package handler

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"naroom/internal/crypto"
	"naroom/internal/middleware"
)

// SessionInit handles POST /session/init — creates a new principal and issues a session.
// Returns recovery code ONCE. No wallet registration yet.
type sessionInitReq struct {
	Role string `json:"role"`
}

func (h *Handler) SessionInit(w http.ResponseWriter, r *http.Request) {
	var req sessionInitReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "invalid json")
		return
	}
	if req.Role != "client" && req.Role != "peer" {
		writeError(w, 400, "role must be client or peer")
		return
	}

	principalID, rawRecovery, err := h.createPrincipal(req.Role)
	if err != nil {
		writeError(w, 500, "principal creation failed")
		return
	}

	// Placeholder wallet_hash for unregistered principal.
	// Will be replaced by real wallet_hash after /wallet/register.
	placeholderHash := "prn:" + principalID

	token, err := h.issueSession(principalID, placeholderHash, req.Role, "")
	if err != nil {
		writeError(w, 500, "session creation failed")
		return
	}

	writeJSON(w, 201, map[string]any{
		"session_token": token,
		"recovery_code": rawRecovery,
		"expires_in":    86400,
		"warning":       "Save your recovery code — it will not be shown again",
	})
}

// SessionRecover handles POST /session/recover — validates recovery code, issues new session.
// Rotates recovery code and revokes all old sessions for this principal.
type sessionRecoverReq struct {
	RecoveryCode string `json:"recovery_code"`
}

func (h *Handler) SessionRecover(w http.ResponseWriter, r *http.Request) {
	var req sessionRecoverReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "invalid json")
		return
	}
	req.RecoveryCode = strings.TrimSpace(req.RecoveryCode)
	if req.RecoveryCode == "" {
		writeError(w, 400, "recovery_code required")
		return
	}

	recoveryHash := crypto.WalletHash(h.HashKey, req.RecoveryCode)

	// Generate new recovery code BEFORE opening the transaction.
	newRaw := make([]byte, 32)
	if _, err := rand.Read(newRaw); err != nil {
		writeError(w, 500, "recovery rotation failed")
		return
	}
	newRawHex := hex.EncodeToString(newRaw)
	newRecoveryHash := crypto.WalletHash(h.HashKey, newRawHex)

	now := time.Now().Unix()

	tx, err := h.DB.Begin()
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// CAS: atomically claim the recovery code (old hash → new hash).
	// Only one concurrent request can win — the rest see 0 rows affected.
	res, err := tx.Exec(`
		UPDATE principals SET recovery_hash = ?, last_seen = ?
		WHERE recovery_hash = ?
	`, newRecoveryHash, now, recoveryHash)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, 401, "invalid recovery code")
		return
	}

	// Read principal after CAS (we know it exists now because CAS succeeded).
	var principalID, role, currency string
	var walletHash sql.NullString
	err = tx.QueryRow(`
		SELECT id, role, COALESCE(currency,''), wallet_hash FROM principals
		WHERE recovery_hash = ?
	`, newRecoveryHash).Scan(&principalID, &role, &currency, &walletHash)
	if err != nil {
		writeError(w, 500, "db error")
		return
	}

	// Revoke all existing sessions for this principal (within tx)
	if _, err := tx.Exec(`UPDATE sessions SET revoked_at = ? WHERE principal_id = ? AND revoked_at IS NULL`,
		now, principalID); err != nil {
		writeError(w, 500, "db error")
		return
	}

	// Use billing wallet_hash if set, otherwise placeholder
	sessionWalletHash := "prn:" + principalID
	if walletHash.Valid && walletHash.String != "" {
		sessionWalletHash = walletHash.String
	}

	// Issue new session inside the transaction
	token, err := h.issueSessionTx(tx, principalID, sessionWalletHash, role, currency)
	if err != nil {
		writeError(w, 500, "session creation failed")
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(w, 500, "db error")
		return
	}

	writeJSON(w, 200, map[string]any{
		"session_token": token,
		"recovery_code": newRawHex,
		"expires_in":    86400,
		"role":          role,
		"warning":       "Your new recovery code — save it, the old one is now invalid",
	})
}

// issueSessionTx creates a new session token within an existing DB transaction.
// Used by SessionRecover to keep the entire recovery atomic.
func (h *Handler) issueSessionTx(tx interface {
	Exec(query string, args ...any) (sql.Result, error)
}, principalID, walletHash, role, currency string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hashBytes := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(hashBytes[:])

	now := time.Now().Unix()
	expiresAt := now + 86400

	var nullPrincipal sql.NullString
	if principalID != "" {
		nullPrincipal = sql.NullString{String: principalID, Valid: true}
	}

	_, err := tx.Exec(`
		INSERT INTO sessions (token_hash, wallet_hash, currency, role, created_at, expires_at, principal_id)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, tokenHash, walletHash, currency, role, now, expiresAt, nullPrincipal)
	if err != nil {
		return "", err
	}
	return token, nil
}

// SessionStatus handles GET /session/status — returns the current session's identity.
// Used by the frontend to validate stored tokens and check wallet link status.
func (h *Handler) SessionStatus(w http.ResponseWriter, r *http.Request) {
	principalID := middleware.SessionPrincipalID(r.Context())
	role := middleware.SessionRole(r.Context())
	walletHash := middleware.SessionWalletHash(r.Context())

	walletLinked := principalID != "" && !strings.HasPrefix(walletHash, "prn:")

	writeJSON(w, 200, map[string]any{
		"principal_id":  principalID,
		"role":          role,
		"wallet_linked": walletLinked,
	})
}
