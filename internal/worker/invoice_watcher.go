package worker

import (
	"context"
	"database/sql"
	"log"
	"math"
	"strconv"
	"time"

	ncrypto "naroom/internal/crypto"
	"naroom/internal/telegram"
)

// InvoiceWatcher checks pending invoices for incoming payments.
type InvoiceWatcher struct {
	DB      *sql.DB
	HashKey []byte // HMAC key for WalletHash — matches handler.HashKey
	Mempool     *ncrypto.MempoolClient
	Blockcypher *ncrypto.BlockcypherClient
	Prices      PriceFetcher // implemented by *ncrypto.PriceCache; interface for testability
	Interval    time.Duration
	DevMode      bool
	SkipPayments bool // auto-confirm all invoices without blockchain checks
	ListingTTL   int
	ChatTTL      int

	// Balance thresholds for post-payment verification.
	// Zero means use code defaults (150 client, 1000 peer).
	// Set via config from CLIENT_MIN_BALANCE_USD / PEER_MIN_BALANCE_USD.
	ClientMinBalanceUSD float64
	PeerMinBalanceUSD   float64

	// Telegram support — set when bot tokens are configured.
	// When RequireTelegram is true, listings only activate after BOTH payment
	// AND Telegram binding are confirmed. When false, payment alone activates
	// (dev mode and deployments without Telegram configured).
	RequireTelegram bool
	TelegramSender  telegram.Sender
	PublicBaseURL   string
}

func (iw *InvoiceWatcher) clientMinBalance() float64 {
	if iw.ClientMinBalanceUSD > 0 {
		return iw.ClientMinBalanceUSD
	}
	return 150.0
}

func (iw *InvoiceWatcher) peerMinBalance() float64 {
	if iw.PeerMinBalanceUSD > 0 {
		return iw.PeerMinBalanceUSD
	}
	return 1000.0
}

func (iw *InvoiceWatcher) Run(ctx context.Context) {
	log.Printf("invoice_watcher started (interval %s)", iw.Interval)
	ticker := time.NewTicker(iw.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("invoice_watcher stopped")
			return
		case <-ticker.C:
			iw.watch(ctx)
		}
	}
}

func (iw *InvoiceWatcher) watch(ctx context.Context) {
	rows, err := iw.DB.Query(`
		SELECT id, type, address, amount_crypto, currency, listing_id, response_id, client_pubkey, payer_address, created_at, payment_detected_at, price_at_creation
		FROM invoices
		WHERE status = 'pending'
	`)
	if err != nil {
		log.Printf("invoice_watcher query error: %v", err)
		return
	}
	defer rows.Close()

	type invoice struct {
		id                string
		typ               string
		address           string
		amountCrypto      string
		currency          string
		listingID         sql.NullString
		responseID        sql.NullString
		clientPubkey      sql.NullString
		payerAddress      sql.NullString
		createdAt         int64
		paymentDetectedAt sql.NullInt64
		priceAtCreation   sql.NullFloat64
	}

	var invoices []invoice
	for rows.Next() {
		var inv invoice
		if err := rows.Scan(&inv.id, &inv.typ, &inv.address, &inv.amountCrypto,
			&inv.currency, &inv.listingID, &inv.responseID, &inv.clientPubkey, &inv.payerAddress,
			&inv.createdAt, &inv.paymentDetectedAt, &inv.priceAtCreation); err != nil {
			continue
		}
		invoices = append(invoices, inv)
	}
	rows.Close()

	now := time.Now().Unix()

	for _, inv := range invoices {
		select {
		case <-ctx.Done():
			return
		default:
		}

		time.Sleep(200 * time.Millisecond) // rate limit

		// Expiry logic:
		//   Normal: expire after 1 hour if no payment detected.
		//   Bounded grace: if a payment was detected but balance/price API is down,
		//   give an extra 24h from detection time before expiring.
		//   This prevents punishing valid payments during a temporary API outage.
		expiryDeadline := inv.createdAt + 3600
		if inv.paymentDetectedAt.Valid {
			grace := inv.paymentDetectedAt.Int64 + 86400
			if grace > expiryDeadline {
				expiryDeadline = grace
			}
		}
		if now > expiryDeadline {
			iw.DB.Exec(`UPDATE invoices SET status = 'expired' WHERE id = ?`, inv.id)
			log.Printf("invoice_watcher: expired invoice %s (type=%s)", inv.id, inv.typ)
			continue
		}

		// Dev mode or SkipPayments: автоматически подтверждаем все pending invoices
		if iw.DevMode || iw.SkipPayments {
			iw.confirmInvoice(inv.id, inv.typ, "dev_txid_"+inv.id, 1000000,
				inv.listingID.String, inv.responseID.String, inv.clientPubkey.String, "")
			continue
		}

		// Check for confirmed payment.
		// Pass 99% of the expected amount as minSatoshis — FindPayment skips dust and
		// other sub-threshold TXs without ever returning them, so a dust TX cannot
		// block detection of the real payment or cause a premature rejection.
		expectedSats := satoshisFromCryptoStr(inv.amountCrypto)
		minRequired := int64(math.Round(float64(expectedSats) * 0.99)) // 1% underpayment tolerance
		if minRequired < 1 {
			minRequired = 1
		}

		if inv.currency == "BTC" {
			tx, amount, senders, err := iw.Mempool.FindPayment(inv.address, minRequired)
			if err != nil {
				log.Printf("invoice_watcher: BTC check error for %s: %v", inv.id, err)
				continue
			}
			if tx != nil {
				// Payment of the right size found on-chain — record detection time for bounded grace.
				// This ensures API outages don't expire valid payments within 24h of detection.
				if !inv.paymentDetectedAt.Valid {
					iw.DB.Exec(`UPDATE invoices SET payment_detected_at = ? WHERE id = ? AND payment_detected_at IS NULL`, now, inv.id)
				}
				senderHash, ok := iw.resolveSender(inv.id, inv.typ, inv.currency, inv.payerAddress.String, senders, inv.priceAtCreation.Float64)
				if !ok {
					continue
				}
				iw.confirmInvoice(inv.id, inv.typ, tx.TxID, amount,
					inv.listingID.String, inv.responseID.String, inv.clientPubkey.String, senderHash)
			}
		} else {
			tx, amount, senders, err := iw.Blockcypher.FindPayment(inv.address, minRequired)
			if err != nil {
				log.Printf("invoice_watcher: LTC check error for %s: %v", inv.id, err)
				continue
			}
			if tx != nil {
				// Payment of the right size found on-chain — record detection time for bounded grace.
				if !inv.paymentDetectedAt.Valid {
					iw.DB.Exec(`UPDATE invoices SET payment_detected_at = ? WHERE id = ? AND payment_detected_at IS NULL`, now, inv.id)
				}
				senderHash, ok := iw.resolveSender(inv.id, inv.typ, inv.currency, inv.payerAddress.String, senders, inv.priceAtCreation.Float64)
				if !ok {
					continue
				}
				iw.confirmInvoice(inv.id, inv.typ, tx.Hash, amount,
					inv.listingID.String, inv.responseID.String, inv.clientPubkey.String, senderHash)
			}
		}
	}
}

// satoshisFromCryptoStr converts a human-readable crypto amount string (e.g. "0.00045678")
// to satoshis/litoshis (integer, 8 decimal places).
func satoshisFromCryptoStr(s string) int64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(math.Round(f * 1e8))
}

func (iw *InvoiceWatcher) confirmInvoice(invoiceID, typ, txid string, amount int64,
	listingID, responseID, clientPubkey string, senderHash string) {

	// Fetch expected amount and currency before confirming.
	var amountCrypto, currency string
	err := iw.DB.QueryRow(`SELECT amount_crypto, currency FROM invoices WHERE id = ?`, invoiceID).
		Scan(&amountCrypto, &currency)
	if err != nil {
		log.Printf("invoice_watcher: cannot fetch invoice %s for amount check: %v", invoiceID, err)
		return
	}

	// In dev mode we skip the amount check (mocked payments send dummy amounts).
	if !iw.DevMode {
		expected := satoshisFromCryptoStr(amountCrypto)
		// Allow up to 1% underpayment (mempool fee fluctuation).
		minAccepted := int64(math.Round(float64(expected) * 0.99))
		if amount < minAccepted {
			log.Printf("invoice_watcher: dust payment for invoice %s: got %d satoshis, need %d (expected %s %s)",
				invoiceID, amount, expected, amountCrypto, currency)
			return
		}
	}

	log.Printf("invoice_watcher: confirmed %s (type=%s, txid=%s, amount=%d sat)", invoiceID, typ, txid, amount)

	tx, err := iw.DB.Begin()
	if err != nil {
		log.Printf("invoice_watcher: begin tx for invoice %s: %v", invoiceID, err)
		return
	}
	defer tx.Rollback() //nolint:errcheck

	res, err := tx.Exec(`UPDATE invoices SET status = 'confirmed', txid = ? WHERE id = ? AND status = 'pending'`,
		txid, invoiceID)
	if err != nil {
		log.Printf("invoice_watcher: mark confirmed invoice %s: %v", invoiceID, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Invoice was already confirmed/expired by a concurrent worker — skip all side effects.
		log.Printf("invoice_watcher: invoice %s already processed, skipping", invoiceID)
		return
	}

	now := time.Now().Unix()

	var notifyListingID string      // set when a listing is activated/renewed; notified after commit
	var notifyChatListingID string  // set when a chat room is created; triggers chat opened notification
	var notifyChatCounselorHash string

	switch typ {
	case "listing":
		if listingID == "" {
			log.Printf("invoice_watcher: listing invoice %s has no listing_id", invoiceID)
			return
		}
		// Activate on payment alone — Telegram is notifications only, not a gate.
		{
			ttl := int64(iw.ListingTTL)
			if ttl == 0 {
				ttl = 86400
			}
			var listingErr error
			if senderHash != "" {
				res, err = tx.Exec(`
					UPDATE listings
					SET status = 'active', visible_until = ?, payment_txid = ?,
					    first_activated_at = COALESCE(first_activated_at, ?),
					    wallet_hash = ?
					WHERE id = ? AND status = 'pending'
				`, now+ttl, txid, now, senderHash, listingID)
				listingErr = err
			} else {
				res, err = tx.Exec(`
					UPDATE listings
					SET status = 'active', visible_until = ?, payment_txid = ?,
					    first_activated_at = COALESCE(first_activated_at, ?)
					WHERE id = ? AND status = 'pending'
				`, now+ttl, txid, now, listingID)
				listingErr = err
			}
			if listingErr != nil {
				log.Printf("invoice_watcher: activate listing %s: %v", listingID, listingErr)
				return
			}
			if n, _ := res.RowsAffected(); n > 0 {
				log.Printf("invoice_watcher: listing %s activated", listingID)
				notifyListingID = listingID
			}
		}

	case "listing_renew":
		if listingID == "" {
			log.Printf("invoice_watcher: renew invoice %s has no listing_id", invoiceID)
			return
		}
		// Renew on payment alone — Telegram is notifications only, not a gate.
		{
			ttl := int64(iw.ListingTTL)
			if ttl == 0 {
				ttl = 86400
			}
			res, err := tx.Exec(`
				UPDATE listings
				SET status = 'active',
				    visible_until = ? + ?,
				    renewal_count = COALESCE(renewal_count, 0) + 1
				WHERE id = ? AND status IN ('active', 'expired')
			`, now, ttl, listingID)
			if err != nil {
				log.Printf("invoice_watcher: renew listing %s: %v", listingID, err)
				return
			}
			if n, _ := res.RowsAffected(); n > 0 {
				log.Printf("invoice_watcher: listing %s renewed", listingID)
				notifyListingID = listingID
			}
		}

	case "chat":
		if responseID == "" || clientPubkey == "" {
			log.Printf("invoice_watcher: chat invoice %s missing response_id or client_pubkey", invoiceID)
			return
		}

		// Защита от дублей — не создавать комнату дважды (читаем внутри транзакции)
		var existing string
		err := tx.QueryRow(`SELECT chat_room_id FROM invoices WHERE id = ? AND chat_room_id IS NOT NULL`, invoiceID).Scan(&existing)
		if err == nil && existing != "" {
			log.Printf("invoice_watcher: chat room already created for invoice %s", invoiceID)
			return
		}

		// Получить данные из response — counselor_hash уже хранится хешем
		var listingIDFromResp, counselorHash, counselorPubkey string
		err = tx.QueryRow(`
			SELECT listing_id, counselor_hash, counselor_pubkey
			FROM responses WHERE id = ?
		`, responseID).Scan(&listingIDFromResp, &counselorHash, &counselorPubkey)
		if err != nil {
			log.Printf("invoice_watcher: response %s not found: %v", responseID, err)
			return
		}

		// Actual payment sender is the authority for who the counselor is.
		// If senderHash is set and differs from session wallet, rebind.
		counselorHashForRoom := counselorHash
		if senderHash != "" && senderHash != counselorHash {
			log.Printf("invoice_watcher: chat invoice %s: rebinding counselor from session wallet to payment sender", invoiceID)
			counselorHashForRoom = senderHash
		}

		// Получить хеш клиента и счётчик открытых чатов из listing
		var clientHash string
		var openedChatsCount int
		err = tx.QueryRow(`SELECT wallet_hash, COALESCE(opened_chats_count, 0) FROM listings WHERE id = ?`, listingIDFromResp).Scan(&clientHash, &openedChatsCount)
		if err != nil {
			log.Printf("invoice_watcher: listing %s not found: %v", listingIDFromResp, err)
			return
		}

		// Entitlement guard: listing allows at most 2 paid chats
		if openedChatsCount >= 2 {
			log.Printf("invoice_watcher: listing %s already has %d opened chats (max 2), aborting chat room creation for invoice %s",
				listingIDFromResp, openedChatsCount, invoiceID)
			return
		}

		// Проверить вместимость пира: считаем активные chat_rooms (не pending responses)
		var peerActiveChatCount int
		tx.QueryRow(`
			SELECT COUNT(*) FROM chat_rooms
			WHERE counselor_hash = ? AND status IN ('active', 'peer_left', 'client_left')
		`, counselorHashForRoom).Scan(&peerActiveChatCount)
		var peerMinRequired float64
		tx.QueryRow(`SELECT COALESCE(min_required_usd, 1000) FROM wallet_sessions WHERE wallet_hash = ?`, counselorHashForRoom).Scan(&peerMinRequired)
		peerMaxSlots := int(peerMinRequired/1000) * 2
		if peerMaxSlots < 2 {
			peerMaxSlots = 2
		}
		if peerActiveChatCount >= peerMaxSlots {
			log.Printf("invoice_watcher: peer at capacity (%d/%d active chats), aborting chat room creation for invoice %s",
				peerActiveChatCount, peerMaxSlots, invoiceID)
			return
		}

		// Создать chat_room — хранятся хеши, не plain адреса
		roomID := ncrypto.NewID("room")
		chatTTL := int64(iw.ChatTTL)
		if chatTTL == 0 {
			chatTTL = 86400
		}
		_, err = tx.Exec(`
			INSERT INTO chat_rooms (id, listing_id, response_id, client_hash, counselor_hash,
			                        client_pubkey, counselor_pubkey, started_at, expires_at, status, listing_counted)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', 1)
		`, roomID, listingIDFromResp, responseID,
			clientHash, counselorHashForRoom,
			clientPubkey, counselorPubkey,
			now, now+chatTTL)
		if err != nil {
			log.Printf("invoice_watcher: create chat_room: %v", err)
			return
		}

		// Increment opened_chats_count and determine new listing status
		newOpenedCount := openedChatsCount + 1
		var newListingStatus string
		if newOpenedCount >= 2 {
			// Second chat opened — listing is now fully matched, close it
			newListingStatus = "closed"
			log.Printf("invoice_watcher: listing %s reached 2 opened chats, closing (room=%s)", listingIDFromResp, roomID)
		} else {
			// First chat opened — listing stays 'matched' until this chat closes (may reopen for second peer)
			newListingStatus = "matched"
		}

		if _, err = tx.Exec(`
			UPDATE listings SET opened_chats_count = opened_chats_count + 1, status = ?
			WHERE id = ?
		`, newListingStatus, listingIDFromResp); err != nil {
			log.Printf("invoice_watcher: update listing %s: %v", listingIDFromResp, err)
			return
		}

		// Записать room_id в invoice (защита от дублей)
		if _, err = tx.Exec(`UPDATE invoices SET chat_room_id = ? WHERE id = ?`, roomID, invoiceID); err != nil {
			log.Printf("invoice_watcher: set chat_room_id on invoice %s: %v", invoiceID, err)
			return
		}

		log.Printf("invoice_watcher: chat room %s created (listing=%s, response=%s, opened_chats_count=%d)",
			roomID, listingIDFromResp, responseID, newOpenedCount)
		notifyChatListingID = listingIDFromResp
		notifyChatCounselorHash = counselorHashForRoom
	}

	if err := tx.Commit(); err != nil {
		log.Printf("invoice_watcher: commit invoice %s: %v", invoiceID, err)
		return
	}

	// Notify after commit: listing activated → client + matching helpers
	if notifyListingID != "" && iw.TelegramSender != nil {
		lID := notifyListingID
		boardURL := iw.PublicBaseURL + "/board"
		go func() {
			ctx := context.Background()
			if err := telegram.NotifyClientListingActivated(ctx, iw.DB, iw.TelegramSender, lID); err != nil {
				log.Printf("invoice_watcher: notify client listing activated (listing=%s): %v", lID, err)
			}
			if err := telegram.NotifyMatchingHelpers(ctx, iw.DB, iw.TelegramSender, lID, boardURL); err != nil {
				log.Printf("invoice_watcher: notify matching helpers (listing=%s): %v", lID, err)
			}
		}()
	}

	// Notify after commit: chat opened → client + helper
	if notifyChatListingID != "" && iw.TelegramSender != nil {
		lID := notifyChatListingID
		cHash := notifyChatCounselorHash
		go func() {
			if err := telegram.NotifyChatOpened(context.Background(), iw.DB, iw.TelegramSender, lID, cHash); err != nil {
				log.Printf("invoice_watcher: notify chat opened (listing=%s): %v", lID, err)
			}
		}()
	}
}

// resolveSender determines the actual payer for an invoice and checks their balance.
//
// The actual blockchain sender IS the authority — no rejection for "wrong sender".
// If the registered wallet is among the senders it is preferred (no rebind).
// If not, the first sender is used as the actual payer.
//
// Returns (actualSenderHash, true) on success.
// Returns ("", false) if the invoice should be rejected (no senders, bad balance, API error).
//
// In DevMode: returns (payerAddress, true) — use registered hash, no rebind.
//
// priceAtCreation: USD/coin rate stored when the invoice was created (0 if unknown).
// At confirmation we use the more user-favorable of creation price and current price,
// so a price drop between creation and confirmation does not incorrectly fail the gate.
func (iw *InvoiceWatcher) resolveSender(invoiceID, typ, currency, payerAddress string, senders []string, priceAtCreation float64) (string, bool) {
	// DevMode: skip all verification, use registered hash
	if iw.DevMode {
		return payerAddress, true
	}

	// Empty payerAddress means the invoice was created without a registered wallet — data integrity error
	if payerAddress == "" {
		log.Printf("invoice_watcher: empty payer_address for invoice %s — rejecting", invoiceID)
		iw.DB.Exec(`UPDATE invoices SET status = 'rejected' WHERE id = ?`, invoiceID)
		return "", false
	}

	// No senders in transaction inputs — unreadable tx, reject
	if len(senders) == 0 {
		log.Printf("invoice_watcher: no sender addresses in tx for invoice %s — rejecting", invoiceID)
		iw.DB.Exec(`UPDATE invoices SET status = 'rejected' WHERE id = ?`, invoiceID)
		return "", false
	}

	// Determine actual sender: prefer registered wallet if present among senders.
	// Otherwise use the first sender as the actual payer (rebind).
	actualSender := senders[0]
	for _, s := range senders {
		if ncrypto.WalletHash(iw.HashKey, s) == payerAddress {
			// Registered wallet found — prefer it, no rebind needed
			actualSender = s
			break
		}
	}

	actualSenderHash := ncrypto.WalletHash(iw.HashKey, actualSender)

	// Balance check: sender must still hold the required minimum AFTER paying the invoice.
	//
	// Invariant (intentional product decision):
	//   listing ($5 payment):  client registered with ≥$150 → post-payment check ≥$135 ($150 - $5 - $10 buffer)
	//   chat ($15 payment):    peer registered with ≥$1000 → post-payment check ≥$975 ($1000 - $15 - $10 buffer)
	//
	// The lower post-payment threshold is by design — we do not penalize users for the platform fee itself.
	// The $10 buffer covers price volatility in the ~30s poll interval. This is a heuristic, not a hard guarantee.
	if iw.Prices == nil {
		return actualSenderHash, true // no price client configured, skip balance check
	}

	invoiceCost := 5.0
	minHold := iw.clientMinBalance()
	if typ == "chat" {
		invoiceCost = 15.0
		minHold = iw.peerMinBalance()
	}
	minUSD := minHold - invoiceCost - 10.0 // subtract invoice cost + $10 volatility buffer

	var balanceUSD float64
	if currency == "BTC" {
		sat, err := iw.Mempool.GetBalance(actualSender)
		if err != nil {
			log.Printf("invoice_watcher: BTC balance check failed for invoice %s: %v — leaving pending", invoiceID, err)
			return "", false // leave pending, retry next cycle
		}
		currentPrice, err := iw.Prices.BTCPrice()
		if err != nil {
			log.Printf("invoice_watcher: BTC price unavailable for invoice %s: %v — leaving pending", invoiceID, err)
			return "", false // leave pending, retry next cycle
		}
		// Use the more favorable price (higher = more USD per coin = higher apparent balance).
		// This protects users from price drops between invoice creation and confirmation.
		price := currentPrice
		if priceAtCreation > price {
			price = priceAtCreation
		}
		balanceUSD = float64(sat) / 1e8 * price
	} else {
		lit, err := iw.Blockcypher.GetBalance(actualSender)
		if err != nil {
			log.Printf("invoice_watcher: LTC balance check failed for invoice %s: %v — leaving pending", invoiceID, err)
			return "", false // leave pending, retry next cycle
		}
		currentPrice, err := iw.Prices.LTCPrice()
		if err != nil {
			log.Printf("invoice_watcher: LTC price unavailable for invoice %s: %v — leaving pending", invoiceID, err)
			return "", false // leave pending, retry next cycle
		}
		price := currentPrice
		if priceAtCreation > price {
			price = priceAtCreation
		}
		balanceUSD = float64(lit) / 1e8 * price
	}

	if balanceUSD < minUSD {
		log.Printf("invoice_watcher: insufficient balance for invoice %s: sender has $%.2f, need $%.2f — rejecting",
			invoiceID, balanceUSD, minUSD)
		iw.DB.Exec(`UPDATE invoices SET status = 'rejected' WHERE id = ?`, invoiceID)
		return "", false
	}

	log.Printf("invoice_watcher: sender resolved for invoice %s: balance $%.2f ≥ $%.2f", invoiceID, balanceUSD, minUSD)
	return actualSenderHash, true
}
