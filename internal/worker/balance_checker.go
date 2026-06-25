package worker

import (
	"context"
	"database/sql"
	"log"
	"time"

	ncrypto "naroom/internal/crypto"
)

const gracePeriodSec = 30 * 60 // 30 минут grace period перед fail

// BalanceChecker periodically checks balances of active wallet sessions.
type BalanceChecker struct {
	DB           *sql.DB
	HashKey      []byte // HMAC key for WalletHash — matches handler.HashKey
	WalletEncKey []byte // AES-256-GCM key for decrypting wallet_address_enc
	Mempool      *ncrypto.MempoolClient
	Blockcypher  *ncrypto.BlockcypherClient
	Prices       *ncrypto.PriceCache
	Interval     time.Duration
}

func (bc *BalanceChecker) Run(ctx context.Context) {
	log.Printf("balance_checker started (interval %s)", bc.Interval)
	ticker := time.NewTicker(bc.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("balance_checker stopped")
			return
		case <-ticker.C:
			bc.check(ctx)
		}
	}
}

type walletSession struct {
	walletHash  string
	addrEnc     string
	currency    string
	role        string
	status      string
	minRequired float64
	lowSince    sql.NullInt64
}

func shortHash(h string) string {
	if len(h) <= 8 {
		return h
	}
	return h[:8]
}

func (bc *BalanceChecker) check(ctx context.Context) {
	rows, err := bc.DB.Query(`
		SELECT wallet_hash, wallet_address_enc, currency, role, balance_status, min_required_usd, low_since
		FROM wallet_sessions
		WHERE balance_status IN ('ok', 'low')
	`)
	if err != nil {
		log.Printf("balance_checker query error: %v", err)
		return
	}
	defer rows.Close()

	var sessions []walletSession
	for rows.Next() {
		var s walletSession
		if err := rows.Scan(&s.walletHash, &s.addrEnc, &s.currency, &s.role, &s.status, &s.minRequired, &s.lowSince); err != nil {
			continue
		}
		sessions = append(sessions, s)
	}
	rows.Close()

	// Fetch prices once for the whole iteration
	btcPrice, btcErr := bc.Prices.BTCPrice()
	ltcPrice, ltcErr := bc.Prices.LTCPrice()

	for _, s := range sessions {
		select {
		case <-ctx.Done():
			return
		default:
		}

		time.Sleep(100 * time.Millisecond) // rate limit

		// Decrypt address only when needed for the blockchain API call.
		// Plain address is never stored in memory beyond this scope.
		plainAddr, err := ncrypto.DecryptAddress(bc.WalletEncKey, s.addrEnc)
		if err != nil {
			log.Printf("balance_checker: decrypt error for %s: %v — skipping", shortHash(s.walletHash), err)
			continue
		}

		var balanceSat int64
		var fetchErr error
		var usdBalance float64

		if s.currency == "BTC" {
			if btcErr != nil {
				log.Printf("balance_checker: BTC price unavailable, skipping %s", shortHash(s.walletHash))
				continue
			}
			balanceSat, fetchErr = bc.Mempool.GetBalance(plainAddr)
			if fetchErr != nil {
				log.Printf("balance_checker: %s fetch error: %v (keeping status)", shortHash(s.walletHash), fetchErr)
				continue
			}
			usdBalance = float64(balanceSat) / 1e8 * btcPrice
		} else {
			if ltcErr != nil {
				log.Printf("balance_checker: LTC price unavailable, skipping %s", shortHash(s.walletHash))
				continue
			}
			balanceSat, fetchErr = bc.Blockcypher.GetBalance(plainAddr)
			if fetchErr != nil {
				log.Printf("balance_checker: %s fetch error: %v (keeping status)", shortHash(s.walletHash), fetchErr)
				continue
			}
			usdBalance = float64(balanceSat) / 1e8 * ltcPrice
		}

		now := time.Now().Unix()
		bc.updateStatus(s, plainAddr, usdBalance, now)
	}

	if len(sessions) > 0 {
		log.Printf("balance_checker: checked %d wallets", len(sessions))
	}
}

func (bc *BalanceChecker) updateStatus(s walletSession, plainAddr string, usdBalance float64, now int64) {
	if usdBalance >= s.minRequired {
		if s.status != "ok" {
			bc.DB.Exec(`UPDATE wallet_sessions SET balance_status = 'ok', balance_usd = ?, low_since = NULL, last_checked_at = ? WHERE wallet_hash = ?`,
				usdBalance, now, s.walletHash)
			log.Printf("balance_checker: %s restored to ok (%.2f USD)", shortHash(s.walletHash), usdBalance)
		} else {
			bc.DB.Exec(`UPDATE wallet_sessions SET balance_usd = ?, last_checked_at = ? WHERE wallet_hash = ?`, usdBalance, now, s.walletHash)
		}
		return
	}

	if s.status == "ok" {
		bc.DB.Exec(`UPDATE wallet_sessions SET balance_status = 'low', balance_usd = ?, low_since = ?, last_checked_at = ? WHERE wallet_hash = ?`,
			usdBalance, now, now, s.walletHash)
		log.Printf("balance_checker: %s went low (%.2f USD < %.2f required)", shortHash(s.walletHash), usdBalance, s.minRequired)
		return
	}

	if s.lowSince.Valid && (now-s.lowSince.Int64) >= gracePeriodSec {
		bc.DB.Exec(`UPDATE wallet_sessions SET balance_status = 'fail', balance_usd = ?, last_checked_at = ? WHERE wallet_hash = ?`,
			usdBalance, now, s.walletHash)
		log.Printf("balance_checker: %s FAIL after grace period (%.2f USD)", shortHash(s.walletHash), usdBalance)
		bc.closeChatsAndListings(s.walletHash)
	} else {
		bc.DB.Exec(`UPDATE wallet_sessions SET balance_usd = ?, last_checked_at = ? WHERE wallet_hash = ?`, usdBalance, now, s.walletHash)
	}
}

// closeChatsAndListings closes all active chats and listings for a wallet that failed the balance gate.
// Uses wallet_hash — plain address is never stored in or compared against chats/listings tables.
func (bc *BalanceChecker) closeChatsAndListings(walletHash string) {
	now := time.Now().Unix()

	res, _ := bc.DB.Exec(`
		UPDATE chat_rooms
		SET status = 'closed', closed_at = ?, closed_by = 'balance'
		WHERE status = 'active'
		AND (client_hash = ? OR counselor_hash = ?)
	`, now, walletHash, walletHash)
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("balance_checker: closed %d chats for %s (balance fail)", n, shortHash(walletHash))
	}

	res, _ = bc.DB.Exec(`
		UPDATE listings SET status = 'closed_balance'
		WHERE status = 'active' AND wallet_hash = ?
	`, walletHash)
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("balance_checker: closed %d listings for %s (balance fail)", n, shortHash(walletHash))
	}
}
