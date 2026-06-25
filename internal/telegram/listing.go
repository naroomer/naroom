package telegram

import (
	"database/sql"
	"time"
)

// ActivateListingIfReady activates a pending listing only when both conditions are met:
//   - A confirmed payment invoice exists for the listing.
//   - If requireTelegram is true: an active client Telegram binding exists.
//
// When requireTelegram is false (dev mode or no bot token configured), the listing
// activates on payment alone — matching the previous behaviour and keeping E2E tests intact.
//
// db may be *sql.DB or *sql.Tx; both implement execer.
func ActivateListingIfReady(db execer, listingID string, listingTTL int, requireTelegram bool) (bool, error) {
	now := time.Now().Unix()

	// Gate 1: confirmed payment invoice
	var txid string
	err := queryRow(db, `
		SELECT COALESCE(i.txid, '')
		FROM invoices i
		JOIN listings l ON l.id = i.listing_id
		WHERE i.type = 'listing' AND i.listing_id = ? AND i.status = 'confirmed'
		  AND l.status = 'pending'
		ORDER BY i.created_at DESC
		LIMIT 1
	`, listingID).Scan(&txid)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	// Gate 2 (optional): active Telegram binding
	ttl := int64(listingTTL)
	if ttl == 0 {
		ttl = 21600
	}
	var expiresAt int64

	if requireTelegram {
		var bindingExpires int64
		err = queryRow(db, `
			SELECT expires_at
			FROM client_listing_notifications
			WHERE listing_id = ? AND active = TRUE AND expires_at > ?
			ORDER BY created_at DESC
			LIMIT 1
		`, listingID, now).Scan(&bindingExpires)
		if err == sql.ErrNoRows {
			return false, nil // Telegram not connected yet
		}
		if err != nil {
			return false, err
		}
		expiresAt = bindingExpires
	} else {
		expiresAt = now + ttl
	}

	// Cap to configured ListingTTL
	if expiresAt > now+ttl {
		expiresAt = now + ttl
	}

	res, err := exec(db, `
		UPDATE listings
		SET status = 'active', visible_until = ?, payment_txid = ?,
		    first_activated_at = COALESCE(first_activated_at, ?)
		WHERE id = ? AND status = 'pending'
	`, expiresAt, txid, now, listingID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// RenewListingIfReady applies a confirmed renewal invoice only when a fresh Telegram
// binding exists (created after the renewal invoice). When requireTelegram is false,
// the renewal is applied directly — keeping existing behaviour.
func RenewListingIfReady(db execer, listingID string, listingTTL int, requireTelegram bool) (bool, error) {
	now := time.Now().Unix()

	// Find the latest confirmed renewal invoice for this listing
	var invoiceID string
	var invoiceCreatedAt int64
	err := queryRow(db, `
		SELECT id, created_at FROM invoices
		WHERE type = 'listing_renew' AND listing_id = ? AND status = 'confirmed'
		ORDER BY created_at DESC
		LIMIT 1
	`, listingID).Scan(&invoiceID, &invoiceCreatedAt)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	ttl := int64(listingTTL)
	if ttl == 0 {
		ttl = 21600
	}
	var expiresAt int64

	if requireTelegram {
		// Require a Telegram binding created AFTER the renewal invoice
		var bindingExpires int64
		err = queryRow(db, `
			SELECT expires_at
			FROM client_listing_notifications
			WHERE listing_id = ? AND active = TRUE AND expires_at > ? AND created_at > ?
			ORDER BY created_at DESC
			LIMIT 1
		`, listingID, now, invoiceCreatedAt).Scan(&bindingExpires)
		if err == sql.ErrNoRows {
			return false, nil // Fresh Telegram binding not yet connected
		}
		if err != nil {
			return false, err
		}
		expiresAt = bindingExpires
	} else {
		expiresAt = now + ttl
	}

	if expiresAt > now+ttl {
		expiresAt = now + ttl
	}

	res, err := exec(db, `
		UPDATE listings
		SET status = 'active', visible_until = ?,
		    renewal_count = COALESCE(renewal_count, 0) + 1
		WHERE id = ? AND status IN ('active', 'expired')
	`, expiresAt, listingID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		// Listing not in renewable state — do not consume the invoice.
		return false, nil
	}
	// Extend Telegram notification to match the new listing expiry.
	// This keeps the client notification alive for the full renewed 6-hour window.
	_, _ = exec(db, `
		UPDATE client_listing_notifications
		SET expires_at = ?
		WHERE listing_id = ? AND active = TRUE
	`, expiresAt, listingID)

	if _, err = exec(db, `UPDATE invoices SET status = 'consumed' WHERE id = ?`, invoiceID); err != nil {
		return false, err
	}
	return true, nil
}

// execer is satisfied by both *sql.DB and *sql.Tx.
type execer interface {
	QueryRow(query string, args ...any) *sql.Row
	Exec(query string, args ...any) (sql.Result, error)
}

func queryRow(db execer, query string, args ...any) *sql.Row {
	return db.QueryRow(query, args...)
}

func exec(db execer, query string, args ...any) (sql.Result, error) {
	return db.Exec(query, args...)
}
