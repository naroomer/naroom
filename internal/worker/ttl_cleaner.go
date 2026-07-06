package worker

import (
	"context"
	"database/sql"
	"log"
	"time"
)

// TTLCleaner periodically removes expired data.
type TTLCleaner struct {
	DB       *sql.DB
	Interval time.Duration
}

func (tc *TTLCleaner) Run(ctx context.Context) {
	log.Printf("ttl_cleaner started (interval %s)", tc.Interval)
	ticker := time.NewTicker(tc.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("ttl_cleaner stopped")
			return
		case <-ticker.C:
			tc.clean()
		}
	}
}

func (tc *TTLCleaner) clean() {
	now := time.Now().Unix()
	var totalCleaned int64

	// 1. Delete expired encrypted messages (24h TTL)
	res, _ := tc.DB.Exec(`DELETE FROM encrypted_messages WHERE created_at + 86400 < ?`, now)
	if n, _ := res.RowsAffected(); n > 0 {
		totalCleaned += n
		log.Printf("ttl_cleaner: deleted %d expired messages", n)
	}

	// 2. Close expired active chat rooms
	res, _ = tc.DB.Exec(`
		UPDATE chat_rooms SET status = 'expired', closed_at = ?, closed_by = 'system'
		WHERE status = 'active' AND expires_at < ?
	`, now, now)
	if n, _ := res.RowsAffected(); n > 0 {
		totalCleaned += n
		log.Printf("ttl_cleaner: expired %d chat rooms", n)
	}

	// 2a. Close accepted responses whose chat room is now expired/closed.
	//     Without this, peers accumulate stale 'accepted' responses that block new slots.
	res, _ = tc.DB.Exec(`
		UPDATE responses SET status = 'closed'
		WHERE status = 'accepted'
		  AND id IN (
		    SELECT response_id FROM chat_rooms
		    WHERE status IN ('expired', 'closed') AND response_id IS NOT NULL
		  )
	`)
	if n, _ := res.RowsAffected(); n > 0 {
		totalCleaned += n
		log.Printf("ttl_cleaner: closed %d stale accepted responses", n)
	}

	// 2c. Expire half-closed rooms (peer_left / client_left) after 24h.
	//     Deletes messages and restores listing.
	tc.expireHalfClosedRooms(now)
	_ = totalCleaned

	// 2b. Delete chat rooms that have been expired/closed for 48h (social graph minimisation).
	//     Messages are already deleted by step 1; this removes the participant link.
	res, _ = tc.DB.Exec(`
		DELETE FROM chat_rooms
		WHERE status IN ('expired', 'closed')
		  AND closed_at IS NOT NULL
		  AND closed_at + 172800 < ?
	`, now)
	if n, _ := res.RowsAffected(); n > 0 {
		totalCleaned += n
		log.Printf("ttl_cleaner: deleted %d old chat room records", n)
	}

	// 3. Expire listings past visible_until
	res, _ = tc.DB.Exec(`
		UPDATE listings SET status = 'expired'
		WHERE status = 'active' AND visible_until < ?
	`, now)
	if n, _ := res.RowsAffected(); n > 0 {
		totalCleaned += n
		log.Printf("ttl_cleaner: expired %d listings", n)
	}

	// 4. Delete expired review tokens
	res, _ = tc.DB.Exec(`DELETE FROM review_tokens WHERE expires_at < ?`, now)
	if n, _ := res.RowsAffected(); n > 0 {
		totalCleaned += n
	}

	// 5. Delete expired abuse dedup records
	res, _ = tc.DB.Exec(`DELETE FROM abuse_dedup WHERE expires_at < ?`, now)
	if n, _ := res.RowsAffected(); n > 0 {
		totalCleaned += n
	}

	// 6. Delete old invoices (48h)
	res, _ = tc.DB.Exec(`DELETE FROM invoices WHERE created_at + 172800 < ?`, now)
	if n, _ := res.RowsAffected(); n > 0 {
		totalCleaned += n
	}

	// 7. Delete wallet sessions whose auth session has fully expired (no valid token).
	// Keep wallet_sessions as long as a live Bearer token exists — balance/reputation
	// data must remain available for the whole session lifetime (24h).
	res, _ = tc.DB.Exec(`
		DELETE FROM wallet_sessions
		WHERE wallet_hash NOT IN (
			SELECT wallet_hash FROM sessions
			WHERE expires_at > ? AND revoked_at IS NULL
		)
	`, now)
	if n, _ := res.RowsAffected(); n > 0 {
		totalCleaned += n
		log.Printf("ttl_cleaner: cleaned %d expired wallet sessions", n)
	}

	// 8. Delete expired/revoked sessions
	res, _ = tc.DB.Exec(`DELETE FROM sessions WHERE expires_at < ? OR revoked_at IS NOT NULL`, now)
	if n, _ := res.RowsAffected(); n > 0 {
		totalCleaned += n
	}

	// 9. Telegram: deactivate expired client notification bindings
	res, _ = tc.DB.Exec(`
		UPDATE client_listing_notifications SET active = FALSE
		WHERE active = TRUE AND expires_at < ?
	`, now)
	if n, _ := res.RowsAffected(); n > 0 {
		totalCleaned += n
	}

	// 10. Telegram: delete used or expired link tokens
	res, _ = tc.DB.Exec(`
		DELETE FROM telegram_link_tokens WHERE used = TRUE OR expires_at < ?
	`, now)
	if n, _ := res.RowsAffected(); n > 0 {
		totalCleaned += n
	}

	// 10. Reject pending responses for expired listings
	res, _ = tc.DB.Exec(`
		UPDATE responses SET status = 'rejected'
		WHERE status = 'pending'
		AND listing_id IN (SELECT id FROM listings WHERE status != 'active')
	`)
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("ttl_cleaner: rejected %d orphaned responses", n)
	}

	if totalCleaned > 0 {
		log.Printf("ttl_cleaner: total cleaned %d records", totalCleaned)
	}
}

// expireHalfClosedRooms closes peer_left and client_left rooms after 24h.
// The remaining participant had 24h to read messages; now they're deleted.
// Listing is restored so the client can find a new peer.
func (tc *TTLCleaner) expireHalfClosedRooms(now int64) {
	grace := int64(86400) // 24h after the room became half-closed
	rows, err := tc.DB.Query(`
		SELECT id, listing_id FROM chat_rooms
		WHERE status IN ('peer_left', 'client_left')
		  AND (
		    (status = 'peer_left'   AND peer_left_at   IS NOT NULL AND peer_left_at   + ? < ?)
		 OR (status = 'client_left' AND client_left_at IS NOT NULL AND client_left_at + ? < ?)
		 OR expires_at < ?
		  )
	`, grace, now, grace, now, now)
	if err != nil {
		return
	}
	defer rows.Close()

	type room struct{ id, listingID string }
	var expired []room
	for rows.Next() {
		var rm room
		if rows.Scan(&rm.id, &rm.listingID) == nil {
			expired = append(expired, rm)
		}
	}
	rows.Close()

	for _, rm := range expired {
		tx, err := tc.DB.Begin()
		if err != nil {
			continue
		}
		tx.Exec(`UPDATE chat_rooms SET status = 'expired', closed_at = ?, closed_by = 'system' WHERE id = ?`, now, rm.id)
		tx.Exec(`DELETE FROM encrypted_messages WHERE room_id = ?`, rm.id)
		// Restore listing only if it hasn't expired yet
		tx.Exec(`
			UPDATE listings SET status = 'active'
			WHERE id = ? AND status = 'matched' AND visible_until > ?
		`, rm.listingID, now)
		if err := tx.Commit(); err != nil {
			tx.Rollback()
			log.Printf("ttl_cleaner: peer_left expiry tx failed for room %s: %v", rm.id, err)
			continue
		}
		log.Printf("ttl_cleaner: expired peer_left room %s, listing %s restored", rm.id, rm.listingID)
	}
}
