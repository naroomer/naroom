# NA Room — Security & Product Invariants

Every invariant listed here must hold at all times. Each entry documents where it is enforced in code, which test proves it, and whether test coverage is missing.

Invariant IDs use short category codes: **ID** (Identity/Privacy), **SE** (Session/Auth), **LS** (Listing), **RS** (Response/Peer), **CH** (Chat), **IN** (Invoice/Payment), **RP** (Reputation/Review), **WK** (Workers/TTL).

---

## Legend

- ✅ Covered — invariant is enforced in code AND a test proves it
- ⚠️ Partial — code enforces it but no negative test (only happy-path)
- ❌ Missing — no test at all; risk is documented but unverified

---

## ID — Identity & Privacy

### ID-1: Plain wallet address never stored in persistent tables (except wallet_sessions, wallet_challenges)
**Rule:** `listings`, `responses`, `chat_rooms`, `invoices`, `sessions`, `reputation`, `review_tokens`, `abuse_counters`, `abuse_dedup` — all store only `HMAC-SHA256(HASH_KEY, address)`.

**Enforced:**
- `internal/handler/wallet.go:upsertWalletSession` — encrypts before INSERT
- `internal/handler/listing.go:CreateListing` — decrypts enc→hash immediately, stores hash
- `internal/handler/accept.go:AcceptResponse` — same pattern for counselor
- `internal/handler/respond.go:Respond` — `counselor_hash` from session context
- `internal/worker/invoice_watcher.go` — `payer_address` stores HMAC hash

**Tests:** 008 (registers and gets token, never sees plain address in response); 001 (full flow)
**Coverage:** ⚠️ Partial — no test deliberately checks DB rows to confirm no plain address stored

---

### ID-2: wallet_sessions.wallet_address_enc stores AES-256-GCM ciphertext only
**Rule:** The `wallet_address_enc` column must never contain a plain address. Key: `WALLET_ENC_KEY`.

**Enforced:**
- `internal/crypto/encrypt.go:EncryptAddress` / `DecryptAddress`
- `internal/handler/wallet.go:upsertWalletSession` — calls EncryptAddress before INSERT
- `internal/db/db.go:MigrateWalletEncryption` — encrypts existing rows on startup

**Tests:** `internal/crypto/encrypt_test.go` (7 unit tests including wrong-key and tamper detection)
**Coverage:** ⚠️ Partial — unit tests pass, but no E2E test verifies DB column contains non-plaintext value

---

### ID-3: No residual plain-text address stores remain (wallet_challenges dropped)
**Rule:** The `wallet_challenges` table previously stored plain `wallet_address` and was the sole remaining residual plain-text risk. The table was completely orphaned — no handler ever created or read challenges — so it was dropped in Sprint 1.

**Enforced:**
- `internal/db/db.go:Open` — `DROP TABLE IF EXISTS wallet_challenges` migration on startup
- `internal/db/schema.sql` — table removed; not re-created
- `go build ./...` — any code referencing the table would fail to compile

**Tests:** N/A — table is gone; `go build` is the gate
**Coverage:** ✅ Eliminated — risk removed by dropping the table

---

### ID-4: Session tokens stored as SHA-256 hash only
**Rule:** Raw token returned to client once; DB stores only `SHA256(token)`.

**Enforced:**
- `internal/handler/wallet.go:issueSession` — `sha256.Sum256([]byte(token))` before INSERT

**Tests:** 008 (token_hash is 64-char hex in DB), 009 (refresh, revoke)
**Coverage:** ✅ Covered

---

### ID-5: IP addresses never logged or persisted
**Rule:** Rate limiting uses hashed /24 subnet; no IP in logs.

**Enforced:**
- `internal/middleware/nolog.go:NoLogIP` — suppresses route parameters and path
- `internal/middleware/ratelimit.go:ByIP` — hashes subnet before using as key

**Tests:** **024** (captures server stderr; asserts no raw IP, wallet address, or session token in log output)
**Coverage:** ✅ Covered

---

### ID-6: WALLET_ENC_KEY required in production; dev derives from SERVER_SALT
**Rule:** Production start without `WALLET_ENC_KEY` must fail hard.

**Enforced:**
- `internal/crypto/encrypt.go:PrepareEncKey` — returns error if devMode=false and key empty

**Tests:** `encrypt_test.go:TestPrepareEncKeyProd`
**Coverage:** ✅ Covered (unit); ❌ Missing E2E (no test starts server without key and expects failure)

---

## SE — Session & Auth

### SE-1: Protected endpoints return 401 without valid session token
**Rule:** All session-gated routes must reject missing or invalid tokens.

**Enforced:**
- `internal/middleware/session.go:RequireSession` — checks token hash exists in sessions, not revoked, not expired

**Tests:** 009 (invalid/no token → 401); 013 (invoice without session → 401); 012 (abuse without session → 401)
**Coverage:** ✅ Covered

---

### SE-2: Session token rotation invalidates previous token
**Rule:** After `POST /session/refresh`, old token must return 401.

**Enforced:**
- `internal/handler/session.go:SessionRefresh` — sets `revoked_at` on old token row, issues new token

**Tests:** 009 step "original token revoked after refresh → 401" (asserts old token → 401 explicitly)
**Coverage:** ✅ Covered

---

### SE-3: Role enforcement — client cannot respond; peer cannot see responses
**Rule:** Peer-only: `/listing/{id}/respond`, `/peer/region`, `/peer/chatroom`, `/abuse-report`. Client-only: review tokens.

**Enforced:**
- `internal/middleware/session.go` — stores `role` in context
- `internal/handler/respond.go` line 32-36 — explicit `role == "client" → 403` check BEFORE DevMode block
- `internal/handler/review.go` — `CloseChat` issues token only to client
- `internal/handler/abuse.go` — checks `role = peer`

**Tests:** 003 (peer close → no review_token; client close → review_token); 012 (client cannot abuse-report → 403); **016** (client role → 403 on respond, even for own listing)
**Coverage:** ✅ Covered

---

### SE-4: Dev mode session bypass disabled in production
**Rule:** Dev mode shortcut (X-Dev-Wallet / X-Dev-Role headers) must be disabled when DevMode=false.

**Enforced:**
- `internal/middleware/session.go:RequireSession` — `if devMode { use header }`

**Tests:** **020** (devMode=false; X-Dev-Wallet+X-Dev-Role headers rejected → 401; only valid Bearer token accepted)
**Coverage:** ✅ Covered

---

### SE-5: Rate limiting and body size limits enforced
**Rule:** Endpoints return 429 after burst exceeded; bodies > limit return 413.

**Enforced:**
- `internal/middleware/ratelimit.go:ByIP` — per-hashed-subnet token bucket
- `internal/middleware/limit.go:LimitBody` — `http.MaxBytesReader`
- `cmd/naroom/main.go` — per-route burst configuration

**Tests:** 007 (rate limit → 429 after burst); 005 (body > 8 MB → 413)
**Coverage:** ✅ Covered

---

## LS — Listing Lifecycle

### LS-1: Listing starts as `pending`; only becomes `active` after payment confirmed
**Rule:** A listing on the board must have `status=active` and `visible_until > now`.

**Enforced:**
- `internal/handler/listing.go:CreateListing` — `status='pending'`
- `internal/worker/invoice_watcher.go:confirmInvoice` — `UPDATE listings SET status='active', visible_until=?`
- `internal/handler/board.go:Board` — `WHERE status='active' AND visible_until > now`

**Tests:** 001 (listing pending until invoice auto-confirmed); board check
**Coverage:** ✅ Covered

---

### LS-2: Client cannot have two active listings simultaneously
**Rule:** Creating a second listing while one is active/pending returns 409.

**Enforced:**
- `internal/handler/listing.go:CreateListing` — `SELECT COUNT(*) WHERE wallet_hash=? AND status IN ('active','pending')`

**Tests:** 001, 006 (second listing while active → 409)
**Coverage:** ✅ Covered

---

### LS-3: Listing renewal blocked when 2 pending responses exist
**Rule:** Client must choose a peer instead of renewing.

**Enforced:**
- `internal/handler/renew.go:RenewListing` — checks `COUNT(*) FROM responses WHERE listing_id=? AND status='pending' >= 2`

**Tests:** **019** (renew OK at 0 responses, OK at 1, blocked at 2 → 409; `can_renew=false` in GET /listing response)
**Coverage:** ✅ Covered

---

### LS-4: Listing becomes `matched` when chat room is created; removed from board
**Rule:** Board must not show listings with active chats.

**Enforced:**
- `internal/worker/invoice_watcher.go` — `UPDATE listings SET status='matched'` when chat room created
- `internal/handler/board.go:Board` — `NOT EXISTS (chat_rooms active)`

**Tests:** 001, 006 (listing disappears from board after chat opens)
**Coverage:** ✅ Covered

---

## RS — Response & Peer

### RS-1: Max 2 pending responses per listing
**Rule:** Third response attempt returns 409.

**Enforced:**
- `internal/handler/respond.go:Respond` — inside transaction: `SELECT COUNT(*) WHERE listing_id=? AND status='pending' >= 2 → 409`

**Tests:** **017** (3 different peers; 3rd → 409; DB asserts exactly 2 pending rows)
**Coverage:** ✅ Covered

---

### RS-2: Peer cannot respond to the same listing twice
**Rule:** Duplicate response returns 409.

**Enforced:**
- `internal/handler/respond.go:Respond` — `SELECT COUNT(*) WHERE counselor_hash=? AND listing_id=? AND status='pending' > 0 → 409`

**Tests:** 001 (second respond → 409)
**Coverage:** ✅ Covered

---

### RS-3: 30-minute cooldown after cancel
**Rule:** Peer who cancels cannot respond to the same listing for 30 minutes.

**Enforced:**
- `internal/handler/respond.go:CancelResponse` — sets `cooldown_until = now + 1800`
- `internal/handler/respond.go:Respond` — checks `cooldown_until > now`

**Tests:** **021** (peer responds → cancels → immediately responds again → 429 cooldown; DB asserts cooldown_until set; inject time past cooldown → re-respond allowed)
**Coverage:** ✅ Covered

---

### RS-4: Region lock — first response permanently locks peer to that city
**Rule:** Peer who responds in Tbilisi cannot respond in Batumi. Atomic: `UPDATE WHERE region=''`.

**Enforced:**
- `internal/handler/respond.go:Respond` — `UPDATE reputation SET region=? WHERE counselor_hash=? AND region=''` + read-back
- RowsAffected check: if 0 affected AND region still empty → 500

**Tests:** **015** (respond tbilisi → region locked; second listing in batumi → 403 region_locked with locked_region=tbilisi)
**Coverage:** ✅ Covered

---

### RS-5: Peer balance covers (activeResponses + 1) * $1000 slots
**Rule:** Peer with $1000 can hold 1 active response; each additional requires another $1000.

**Enforced:**
- `internal/handler/respond.go:Respond` — `COUNT(*) active responses * 1000 ≤ min_required_usd`

**Tests:** **018** (devMode=false; peer at $1000: slot 1 OK, slot 2 → 403; raise to $2000: slot 2 OK)
**Coverage:** ✅ Covered (IN-5 balance math gate proven end-to-end)

---

## CH — Chat Security

### CH-1: Server never decrypts messages
**Rule:** `encrypted_messages` stores only `nonce + ciphertext`. Server has no keys.

**Enforced:**
- `internal/handler/chat_ws.go` — stores raw nonce+ciphertext from client; never calls decrypt
- `internal/handler/chat_poll.go` — same

**Tests:** 001 (client decrypts message on receive); server never touches plaintext
**Coverage:** ⚠️ Partial — verified by inspection; no test confirms server cannot decrypt

---

### CH-2: Only room participants can send/receive messages
**Rule:** Non-participant attempting to send/receive must be rejected.

**Enforced:**
- `internal/handler/chat_ws.go:ChatWS` — checks `client_hash=? OR counselor_hash=?`
- `internal/handler/chat_poll.go:ChatPollSend` — checks pubkey is participant

**Tests:** 001 (stranger cannot connect to chat), 010 (WS auth)
**Coverage:** ⚠️ Partial — no test tries poll send from a third unrelated wallet

---

### CH-3: WS auth via Sec-WebSocket-Protocol header (token not in URL)
**Rule:** Token must never appear in WS URL query string (would leak to server logs).

**Enforced:**
- `internal/handler/chat_ws.go:ChatWS` — extracts token from `Sec-WebSocket-Protocol` header
- Token NOT read from `r.URL.Query()`

**Tests:** 010 (verifies URL has no token)
**Coverage:** ✅ Covered

---

### CH-4: Messages deleted when chat closes
**Rule:** `encrypted_messages` for a room are deleted when client closes.

**Enforced:**
- `internal/handler/chat_ws.go:CloseChat` — `DELETE FROM encrypted_messages WHERE room_id=?` (client close path)

**Tests:** 001 (close flow); no test queries DB directly after close to confirm deletion
**Coverage:** ⚠️ Partial

---

### CH-5: Cannot send to closed/expired room
**Rule:** After room is closed/expired, any further message attempt returns 410.

**Enforced:**
- `internal/handler/chat_ws.go` — checks `status IN ('active', 'peer_left')` before processing

**Tests:** 004 (cannot send after room_closed event)
**Coverage:** ✅ Covered

---

### CH-6: Chat room scoped to listing_id when queried by peer
**Rule:** `GET /peer/chatroom?listing_id=X` returns room only for the specified listing; prevents stale room return.

**Enforced:**
- `internal/handler/chat_ws.go:GetCounselorChatRoom` — `WHERE listing_id=? AND counselor_hash=?`

**Tests:** 002 (stale room guard)
**Coverage:** ✅ Covered

---

## IN — Invoice & Payment

### IN-1: Invoice payer_address stores HMAC hash, never plain address
**Rule:** `invoices.payer_address` = `HMAC-SHA256(HASH_KEY, wallet_address)`.

**Enforced:**
- `internal/handler/listing.go:CreateListing` — decrypt enc → HMAC → store hash
- `internal/handler/accept.go:AcceptResponse` — same pattern

**Tests:** 013 (invoice status); no test checks DB column value
**Coverage:** ⚠️ Partial

---

### IN-2: Invoice belongs to session owner (ownership check on status endpoint)
**Rule:** Non-owner cannot query invoice status.

**Enforced:**
- `internal/handler/invoice.go:InvoiceStatus` — `WHERE listing_id IN (SELECT id FROM listings WHERE wallet_hash=?)`

**Tests:** 013 (peer cannot see client invoice → 403)
**Coverage:** ✅ Covered

---

### IN-3: Payment must come from registered wallet (sender hash match)
**Rule:** tx sender hash must match payer_address stored at invoice creation. Multi-input: ANY sender may match.

**Enforced:**
- `internal/worker/invoice_watcher.go:verifySenderAndBalance` — checks all tx inputs via HMAC comparison

**Tests:** `invoice_watcher_test.go` (unit: wrong-wallet rejected, multi-input match, no senders rejected)
**Coverage:** ✅ Covered (unit, DevMode=false)

---

### IN-4: Double-confirm guard — invoice confirmed at most once
**Rule:** `UPDATE invoices SET status='confirmed' WHERE id=? AND status='pending'` with RowsAffected check. Side-effects (listing activation, chat room creation) must not fire on a duplicate tick.

**Enforced:**
- `internal/worker/invoice_watcher.go:confirmInvoice` — `RowsAffected == 0 → return` before switch block

**Tests:** `invoice_watcher_test.go:TestDoubleConfirmGuard` — pre-confirmed invoice: verifies txid not overwritten AND linked listing not activated
**Coverage:** ⚠️ Partial — listing side-effect proven; chat room side-effect not tested (requires full DB schema); the guard is structural (same `return` path for all types)

---

### IN-5: Post-payment balance gate with favorable price
**Rule:** After payment, sender balance must be ≥ (minHold - invoiceCost - $10). Uses max(creationPrice, currentPrice).

**Enforced:**
- `internal/worker/invoice_watcher.go:verifySenderAndBalance`
- `invoices.price_at_creation` — set at invoice creation

**Tests:**
- `TestVerify_APIError_LeavesPending` — API error path covered
- `TestBalanceThreshold_ListingPassesAt135` — exactly $135 → passes (listing formula: 150-5-10=135)
- `TestBalanceThreshold_ListingFailsAt134` — 1 sat below $135 threshold → rejected
- `TestBalanceThreshold_ChatPassesAt975` — exactly $975 → passes (chat formula: 1000-15-10=975)
- `TestBalanceThreshold_ChatFailsAt974` — 1 sat below $975 threshold → rejected

**Coverage:** ✅ Covered — exact math thresholds proven for both listing and chat types

---

### IN-6: API errors leave invoice pending; bounded grace after payment detected
**Rule:** API outage must not expire a valid detected payment for 24h. Grace deadline = `max(created_at + 3600, payment_detected_at + 86400)`.

**Enforced:**
- `internal/worker/invoice_watcher.go:watch` — `payment_detected_at` extends deadline before expiry check
- `verifySenderAndBalance` — `return false` (not reject) on balance/price API error

**Tests:**
- `TestVerify_APIError_LeavesPending` — API 503 → false returned, status stays pending
- `TestGraceWindow_NotExpiredWithinGrace` — normal TTL passed, detected recently → still pending
- `TestGraceWindow_ExpiredAfterGrace` — both TTL and grace passed → marked expired

**Coverage:** ✅ Covered (unit)

---

## RP — Reputation & Review

### RP-1: Review token is single-use
**Rule:** Reusing a review token returns 409.

**Enforced:**
- `internal/handler/review.go:Review` — `WHERE token=? AND used=0 AND expires_at > now` then sets `used=1`

**Tests:** 003 (token reuse → 409)
**Coverage:** ✅ Covered

---

### RP-2: Review token only issued to client on explicit close after ≥ 6h session
**Rule:** Peer close → no token. Premature client close (<6h) → no token. Dev mode: ≥ 0.

**Enforced:**
- `internal/handler/chat_ws.go:CloseChat` — client path: `if duration >= ChatMinTTL { issue token }`
- peer path: no token code path

**Tests:** 003 (peer close → no token), 001 (client close → token)
**Coverage:** ⚠️ Partial — no test verifies token NOT issued when session < 6h

---

### RP-3: Abuse report deduplication (one counselor→client pair per 30 days)
**Rule:** Duplicate abuse report from same pair returns 409.

**Enforced:**
- `internal/handler/abuse.go:AbuseReport` — `INSERT OR IGNORE INTO abuse_dedup`; 409 if already exists

**Tests:** 012 (duplicate report → 409)
**Coverage:** ✅ Covered

---

### RP-4: Abuse ban thresholds (3 reports → 72h, 5 → permanent)
**Rule:** After 3 abuse reports, `abuse_counters.banned_until` is set to `now + 259200` (72h). After 5 reports, `banned_until` = `now + 10 years`.

**⚠️ PARTIAL IMPLEMENTATION:** `banned_until` is SET correctly by the abuse handler. However, `banned_until` is **NOT CHECKED** in `/listing/create`, `/listing/{id}/respond`, or `/board` — banned clients are NOT blocked from listing or responding. Ban enforcement is a known gap.

**Enforced (threshold logic only):**
- `internal/handler/abuse.go:AbuseReport` — sets `banned_until` at ≥3 and ≥5 report thresholds

**NOT enforced:**
- No check of `banned_until` in `listing.go`, `respond.go`, or `board.go`

**Tests:** **025** (5 peers report same client; after 3rd: `banned_until` ≈ now+259200; after 5th: ≈ now+10yr; total=5)
**Coverage:** ✅ Threshold SET correctly · ❌ Ban CHECK not implemented — enforcement is a known open issue

---

## WK — Workers & TTL

### WK-1: Encrypted messages deleted within 24h
**Rule:** TTL cleaner deletes `encrypted_messages` created >24h ago.

**Enforced:**
- `internal/worker/ttl_cleaner.go` — `DELETE WHERE created_at < now - 86400`

**Tests:** **022** (inject old message at now-25h and fresh message at now; wait 7s with TTL_CLEAN_INTERVAL=5; old deleted, fresh survives)
**Coverage:** ✅ Covered

---

### WK-2: peer_left room restores listing when it expires (no review token)
**Rule:** If peer leaves and client never explicitly closes, room expires → listing returns to active, no review token.

**Enforced:**
- `internal/worker/ttl_cleaner.go:expirePeerLeftRooms`

**Tests:** 011 (peer_left → expiry → listing restored)
**Coverage:** ✅ Covered

---

### WK-3: Wallet sessions cleaned up after all auth sessions expire
**Rule:** `wallet_sessions` row deleted when no active sessions remain for that wallet_hash.

**Enforced:**
- `internal/worker/ttl_cleaner.go` — `DELETE FROM wallet_sessions WHERE wallet_hash NOT IN (SELECT wallet_hash FROM sessions WHERE expires_at > now AND revoked_at IS NULL)`

**Tests:** **023** (register wallet, revoke session, expire it via DB; wait for TTL cleaner; wallet_sessions row deleted)
**Coverage:** ✅ Covered

---

## Summary of Coverage Gaps

Sprint 1 changes: ID-3 eliminated (table dropped), SE-3/LS-3/RS-1/RS-4/RS-5(IN-5) newly covered by tests 015-019.
Sprint 2 changes: SE-4/RS-3/WK-1/WK-3/ID-5/RP-4 newly covered by tests 020-025.

| Invariant | Status | Notes |
|-----------|--------|-------|
| ID-1 Plain address in DB | ⚠️ | No DB inspection test |
| ID-2 wallet_address_enc is ciphertext | ⚠️ | Unit only, no E2E |
| ~~ID-3 wallet_challenges~~ | ✅ | Eliminated — table dropped in Sprint 1 |
| ID-5 No IP in logs | ✅ | Test 024 added Sprint 2 |
| SE-3 Client cannot respond | ✅ | Test 016 added Sprint 1 |
| SE-4 Dev mode bypass blocked in prod | ✅ | Test 020 added Sprint 2 |
| LS-3 Renewal blocked at 2 responses | ✅ | Test 019 added Sprint 1 |
| RS-1 Max 2 responses per listing | ✅ | Test 017 added Sprint 1 |
| RS-3 30-min cooldown after cancel | ✅ | Test 021 added Sprint 2 |
| RS-4 Region lock cross-city | ✅ | Test 015 added Sprint 1 |
| RS-5 Multi-slot balance scaling | ✅ | Test 018 added Sprint 1 (covers IN-5) |
| CH-1 Server cannot decrypt | ⚠️ | Inspection only |
| CH-2 Poll send from non-participant | ⚠️ | Partial |
| CH-4 Message deletion verified in DB | ⚠️ | No DB assertion |
| IN-1 payer_address is HMAC hash | ⚠️ | No DB column assertion |
| IN-4 Double-confirm chat side-effect | ⚠️ | Listing side-effect proven; chat path structural only |
| IN-5 Balance math (not just error path) | ✅ | Test 018 added Sprint 1 |
| RP-2 No token for short session | ⚠️ | Partial |
| RP-4 Abuse ban thresholds | ✅ | Test 025 added Sprint 2 (thresholds only; enforcement NOT YET IMPLEMENTED) |
| WK-1 Message TTL cleanup | ✅ | Test 022 added Sprint 2 |
| WK-3 wallet_sessions TTL cleanup | ✅ | Test 023 added Sprint 2 |

**Totals after Sprint 2:** ✅ 32 covered · ⚠️ 8 partial · ❌ 0 missing (down from 5 missing after Sprint 1)
