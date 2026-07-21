// dev_helpers.go — exported helpers for the isolated V2 dev runner.
//
// These functions are NOT available to cmd/naroom/main.go (no build tag needed
// because they are only reachable via cmd/naroom-v2-dev which is not imported
// by production binaries). They manipulate the DB directly to simulate external
// events (Telegram bot connections) without real network calls.
package v2

import (
	"database/sql"
	"fmt"
	"time"
)

// DevInsertReadyBinding creates a ready Telegram binding for the given flowID,
// encrypted with destCipher using fakeChatID. Existing bindings for the flow
// are deleted first. For dev/test use only — not called by any production path.
//
// validUntil is set to now + 15 minutes (enough for the dev session).
func DevInsertReadyBinding(db *sql.DB, destCipher *DestinationCipher, flowID string, fakeChatID int64, now time.Time) error {
	nowUnix := now.Unix()
	validUntil := nowUnix + 15*60 // 15 minutes from now

	bindingRef, err := defaultBindingRefGen()
	if err != nil {
		return fmt.Errorf("v2: DevInsertReadyBinding: gen ref: %w", err)
	}

	ctHex, nonceHex, err := destCipher.EncryptChatID(fakeChatID, bindingRef)
	if err != nil {
		return fmt.Errorf("v2: DevInsertReadyBinding: encrypt: %w", err)
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("v2: DevInsertReadyBinding: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Delete any existing ready/active binding for this flow.
	if _, err := tx.Exec(`DELETE FROM v2_client_notification_bindings WHERE flow_id = ?`, flowID); err != nil {
		return fmt.Errorf("v2: DevInsertReadyBinding: delete old: %w", err)
	}

	// Determine window_number from current listing activation count (or 1 if no listing).
	var activationCount sql.NullInt64
	_ = tx.QueryRow(`SELECT activation_count FROM v2_listings WHERE flow_id = ?`, flowID).Scan(&activationCount)
	windowNumber := int64(1)
	if activationCount.Valid {
		windowNumber = activationCount.Int64 + 1
	}

	// Insert binding (state=ready).
	bindingID := newID()
	_, insErr := tx.Exec(`
		INSERT INTO v2_client_notification_bindings
		  (id, flow_id, binding_ref, state, window_number, verified_at, valid_until, created_at, updated_at)
		VALUES (?, ?, ?, 'ready', ?, ?, ?, ?, ?)`,
		bindingID, flowID, bindingRef, windowNumber,
		nowUnix, validUntil, nowUnix, nowUnix,
	)
	if insErr != nil {
		return fmt.Errorf("v2: DevInsertReadyBinding: insert binding: %w", insErr)
	}

	// Insert destination (expires_at = valid_until — enforced by QueryLinkStatus JOIN).
	_, destErr := tx.Exec(`
		INSERT INTO v2_telegram_destinations
		  (binding_ref, chat_id_ciphertext, chat_id_nonce, key_version, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		bindingRef, ctHex, nonceHex, destCipher.keyVersion, nowUnix, validUntil,
	)
	if destErr != nil {
		return fmt.Errorf("v2: DevInsertReadyBinding: insert destination: %w", destErr)
	}

	return tx.Commit()
}

// DevParseTelegramCallbackData parses callback_data from a captured review notification
// and returns (reviewRef, rating, ok). For dev/test use only.
// Format: "rv:" + 32 hex chars + ":" + ("p"|"n")
func DevParseTelegramCallbackData(data string) (reviewRef string, rating string, ok bool) {
	ref, isPositive, parsed := parseTelegramCallbackData(data)
	if !parsed {
		return "", "", false
	}
	r := "negative"
	if isPositive {
		r = "positive"
	}
	return ref, r, true
}
