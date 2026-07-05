# NA Room — Test Coverage Matrix

Maps every test to the invariants it proves. Invariant IDs match `docs/INVARIANTS.md` exactly.

Categories: **ID** (Identity/Privacy) · **SE** (Session/Auth) · **LS** (Listing) · **RS** (Response/Peer) · **CH** (Chat) · **IN** (Invoice/Payment) · **RP** (Reputation/Review) · **WK** (Workers/TTL)

---

## Unit Tests

### `internal/crypto/verify_test.go`

| Test | Invariant(s) | What it proves |
|------|-------------|----------------|
| `TestVerifyBTCMessage_P2PKH` | SE-1 | Valid BTC P2PKH signature accepted |
| `TestVerifyBTCMessage_P2WPKH` | SE-1 | Valid BTC segwit (bech32) signature accepted |
| `TestVerifyBTCMessage_WrongAddress` | SE-1 | Right sig, wrong address → rejected |
| `TestVerifyBTCMessage_WrongMessage` | SE-1 | Valid sig, wrong message → rejected |
| `TestVerifyLTCMessage_P2PKH` | SE-1 | Valid LTC signature accepted |
| `TestVerifyBTCMessage_InvalidBase64` | SE-1 | Non-base64 signature → error, no panic |
| `TestVerifyBTCMessage_ShortSignature` | SE-1 | Signature < 65 bytes → rejected |

### `internal/handler/wallet_challenge_test.go`

> **NOTE:** `/wallet/challenge` is intentionally absent from the architecture. Challenge-signature (Bitcoin message signing) is not required or planned. Wallet control is proven at payment time by sender address verification (`invoice_watcher.go:verifySenderAndBalance`). The `wallet_challenge_test.go` file no longer exists. The tests below are superseded by E2E 027 T1-T4 and 035 T1/T4/T5.

| Test | Invariant(s) | Status |
|------|-------------|--------|
| `TestChallenge_OneTimeUse` | SE-1 | **REMOVED** — challenge-signature not implemented by design |
| `TestChallenge_Expired` | SE-1 | **REMOVED** |
| `TestChallenge_AddressBinding` | SE-1 | **REMOVED** |
| `TestChallenge_ConcurrentReplay` | SE-1 | **REMOVED** |

### `internal/crypto/encrypt_test.go`

| Test | Invariant(s) | What it proves |
|------|-------------|----------------|
| `TestEncryptDecryptRoundTrip` | ID-2 | Encrypt→decrypt = original address |
| `TestDecryptWrongKey` | ID-2 | Wrong key → error, not garbage plaintext |
| `TestDecryptTamperedData` | ID-2 | Bit flip on ciphertext → GCM auth fail |
| `TestEncryptProducesUnique` | ID-2 | Same plaintext → different ciphertext (random nonce) |
| `TestPrepareEncKeyDev` | ID-2, ID-6 | Dev mode without WALLET_ENC_KEY → stable derived key |
| `TestPrepareEncKeyProd` | ID-6 | Prod without WALLET_ENC_KEY → fatal (no silent fallback) |
| `TestDecryptTooShort` | ID-2 | Truncated input → error, no panic |

### `internal/worker/invoice_watcher_test.go`

| Test | Invariant(s) | What it proves |
|------|-------------|----------------|
| `TestVerify_EmptyPayerAddress` | IN-3 | Empty payer_address → invoice rejected |
| `TestVerify_NoSenders` | IN-3 | No senders in tx → invoice rejected |
| `TestVerify_WrongWallet` | IN-3 | Sender hash mismatch → invoice rejected |
| `TestVerify_MultiInputOneMatches` | IN-3 | Multi-input tx: one sender matches → accepted |
| `TestVerify_APIError_LeavesPending` | IN-5, IN-6 | Balance API 503 → returns false, status stays pending (not rejected) |
| `TestDoubleConfirmGuard` | IN-4 | Already-confirmed invoice: txid not overwritten AND linked listing not activated |
| `TestGraceWindow_NotExpiredWithinGrace` | IN-6 | Normal TTL expired, payment detected recently → still pending (grace active) |
| `TestGraceWindow_ExpiredAfterGrace` | IN-6 | Both normal TTL and grace window expired → status='expired' |
| `TestBalanceThreshold_ListingPassesAt135` | IN-5 | Listing: exactly $135 remaining balance → passes gate (minHold=150, cost=5, buffer=10) |
| `TestBalanceThreshold_ListingFailsAt134` | IN-5 | Listing: $134.999 (1 sat below threshold) → rejected |
| `TestBalanceThreshold_ChatPassesAt975` | IN-5 | Chat: exactly $975 remaining balance → passes gate (minHold=1000, cost=15, buffer=10) |
| `TestBalanceThreshold_ChatFailsAt974` | IN-5 | Chat: $974.999 (1 sat below threshold) → rejected |

---

## E2E Tests

### `tests/001_happy_path.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| `/wallet/register` returns `session_token` | SE-1, ID-4 |
| `/listing/create` returns `listing_id` | LS-1 |
| Listing visible on `/board/{city}` | LS-1, LS-2 |
| Second listing while active → 409 | LS-2 |
| `/listing/{id}/respond` by peer succeeds | RS-2 |
| Second respond same peer → 409 | RS-2 |
| Client sees response in `/listing/{id}/responses` | RS-2 |
| `/response/{id}/accept` creates invoice | IN-1, IN-2 |
| Poll `GET /invoice/{id}/status` until confirmed (DevMode fast-confirm) | IN-2 |
| `GET /listing/{id}/chatroom` returns room | CH-2, LS-4 |
| WebSocket connect, send+receive encrypted message | CH-2, CH-3 |
| `/chat/{room_id}/close` by client → status closed | CH-4 |
| Messages deleted after close | CH-4 |
| Review token issued to client | RP-1, RP-2 |
| `POST /review` with valid token succeeds | RP-1 |
| Second POST with same token → 403 | RP-1 |

---

### `tests/002_stale_room_guard.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Old room exists for client | CH-2 |
| Client creates new listing | LS-1 |
| `GET /listing/{id}/chatroom` → no stale room returned | CH-6 |

---

### `tests/003_role_separation_review.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Close chat as client → `review_token` present | RP-2 |
| Close chat as peer → `review_token` absent | RP-2 |
| Reuse review token → 409 | RP-1 |

---

### `tests/004_remote_close_state.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Client closes room | CH-4, CH-5 |
| Peer polls and sees `closed` status | CH-4 |
| Peer WS receives close event; cannot send after | CH-5 |

---

### `tests/005_large_image_payload.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Image data URL within 8 MB → accepted | CH-2 |
| JSON body > 64 KB on non-chat endpoint → 413 | SE-5 |

---

### `tests/006_state_bleed.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Flow A and Flow B share DB, run in sequence | LS-1, RS-2 |
| Each flow sees only its own listing/chat | CH-2, LS-2 |
| Close of Flow A does not affect Flow B | CH-4 |

---

### `tests/007_rate_limiting.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Burst 10 requests to `/wallet/register` → all 400 (not rate-limited) | SE-5 |
| 11th request → 429 | SE-5 |

Note: runs with `devMode: false` specifically for rate limiting to be active.

---

### `tests/008_wallet_challenge.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| POST `/wallet/register` (client) → `session_token` | SE-1, ID-4 |
| POST `/wallet/register` (peer) → `session_token` | SE-1, ID-4 |
| Re-registration same wallet → updates session | ID-4 |
| Missing `wallet_address` field → 400 | SE-1 |
| Invalid signature → 401 | SE-1 |

---

### `tests/009_session_lifecycle.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Fresh token: authenticated request succeeds | SE-1 |
| No token → 401 | SE-1 |
| Invalid token → 401 | SE-1 |
| POST `/session/refresh` → new token returned | SE-2 |
| **Old token after refresh → 401** (asserted explicitly) | **SE-2** |
| New refreshed token works | SE-2 |
| POST `/session/revoke` → token invalidated | SE-1 |
| Revoked token → 401 | SE-1 |
| DB: `revoked_at` set for revoked session | ID-4 |

---

### `tests/010_ws_auth.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| WS connect with valid token in protocol header → accepted | CH-3, SE-1 |
| WS connect without token → 401/close | CH-3, SE-1 |
| WS connect with invalid token → close | CH-3, SE-1 |
| Authenticated WS: messages routed only to correct room | CH-2 |

---

### `tests/011_peer_left_expiry.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Peer closes WS → room status `peer_left` | CH-5 |
| After TTL: room status `closed` | WK-2 |
| Listing restored to `active` | WK-2, LS-1 |
| No review token issued (peer_left is not a clean close) | RP-2 |

---

### `tests/012_abuse_report.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Reporter without prior room → 403 | RP-3 |
| Reporter with prior room → 200 | RP-3 |
| Duplicate report same room → 409 | RP-3 |

---

### `tests/013_invoice_scoping.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| No session → 401 | SE-1 |
| Valid session, wrong wallet → 403 | IN-2 |
| Valid session, correct owner → 200 | IN-2 |

---

### `tests/014_reputation.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Fresh peer: reputation row with `sessions_completed=0` | RP-2, RS-4 |
| After complete session: `sessions_completed` incremented | RP-2 |
| Board shows peer with region and reputation | RS-4 |
| Review thumbs-up/down recorded | RP-1 |

---

## New Tests — Sprint 1

### `tests/015_region_lock.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Peer responds to tbilisi listing → 201 | RS-4 |
| GET /peer/region returns tbilisi | RS-4 |
| Same peer tries batumi listing → 403 region_locked, locked_region=tbilisi | RS-4 |

---

### `tests/016_role_separation_respond.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Client B (role=client) tries respond → 403 | SE-3 |
| Client A (listing owner, role=client) tries own listing respond → 403 | SE-3 |
| Listing still active after rejected responds | SE-3, LS-1 |

---

### `tests/017_max_responses.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Peer A responds → 201 (slot 1) | RS-1 |
| Peer B responds → 201 (slot 2) | RS-1 |
| Peer C responds → 409 (max reached) | RS-1 |
| DB asserts exactly 2 pending response rows | RS-1 |

---

### `tests/018_balance_threshold.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| devMode=false; peer starts at min_required_usd=1000 | IN-5, RS-5 |
| Slot 1 (needs $1000, has $1000) → 201 | IN-5, RS-5 |
| Slot 2 (needs $2000, has $1000) → 403 | IN-5, RS-5 |
| Raise min_required_usd to $2000; slot 2 → 201 | IN-5, RS-5 |

---

### `tests/019_renewal_blocked.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Renew at 0 responses → 200 | LS-3 |
| Peer A responds (slot 1) | RS-1, LS-3 |
| Renew at 1 response → 200 | LS-3 |
| Peer B responds (slot 2) | RS-1, LS-3 |
| Renew at 2 responses → 409 | LS-3 |
| GET /listing/{id} shows can_renew=false | LS-3 |

---

## New Tests — Sprint 2

### `tests/020_devmode_headers.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| devMode=false; X-Dev-Wallet + X-Dev-Role headers → 401 (no access) | SE-4 |
| Same request with valid Bearer token (registerDirect) → 200 | SE-4 |

---

### `tests/021_cancel_cooldown.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Peer responds to listing → 201 | RS-3 |
| Peer cancels response → 200 | RS-3 |
| Peer immediately responds to same listing → 429 cooldown_active | RS-3 |
| DB: cooldown_until set on cancelled response row | RS-3 |
| Inject DB time past cooldown; re-respond → 201 | RS-3 |

---

### `tests/022_message_ttl.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Inject message at now-25h and message at now | WK-1 |
| Wait 7s (TTL_CLEAN_INTERVAL=5s) | WK-1 |
| Old message (25h) → deleted | WK-1 |
| Fresh message (0h) → still present | WK-1 |

---

### `tests/023_wallet_session_ttl.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Register wallet → session row exists; wallet_sessions row exists | WK-3 |
| Revoke session; expire it via DB injection | WK-3 |
| Wait for TTL cleaner | WK-3 |
| wallet_sessions row deleted (no active sessions remain) | WK-3 |

---

### `tests/024_log_privacy.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Capture server stderr during normal operations | ID-5 |
| Assert no raw IP addresses in logs | ID-5 |
| Assert no wallet addresses in logs | ID-5 |
| Assert no raw session tokens in logs | ID-5 |

---

### `tests/025_abuse_ban.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Inject 5 closed chat_rooms (one per peer) | RP-4 |
| Peers 1-3 submit abuse reports → 200 | RP-4 |
| After 3rd report: banned_until ≈ now + 259200 (72h) | RP-4 |
| Peers 4-5 submit abuse reports → 200 | RP-4 |
| After 5th report: banned_until ≈ now + 10 years | RP-4 |
| abuse_counters.total = 5 | RP-4 |

---

### `tests/036_ban_enforcement.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| T1: banned wallet cannot respond to listing → 403 + error="account banned" | RP-4 |
| T2: banned wallet cannot create listing → 403 + error="account banned" | RP-4 |
| T3: banned wallet cannot renew listing → 403 + error="account banned" | RP-4 |
| T4: banned wallet cannot send chat poll message → 403 + error="account banned" | RP-4 |
| T5: banned wallet cannot update chat pubkey → 403 + error="account banned" | RP-4 |
| T6: non-banned wallet can still create listing → 201 (sanity) | RP-4 |
| T7: banned peer wallet CAN still submit abuse report → 200 (not blocked) | RP-4 |
| T8: banned wallet CAN access GET /board → 200 (read-only OK) | RP-4 |
| T8: banned wallet CAN access GET /listing → 200 (read-only OK) | RP-4 |
| 403 response includes banned_until timestamp | RP-4 |

---

## Tests — Fable Five Audit Sprint

### `tests/026_analytics_privacy.js` *(Playwright — selftest-full.sh only)*

| Step/Check | Invariant(s) |
|-----------|-------------|
| Browser loads public page (`/`) → GoatCounter loads | ID-5 |
| Browser loads `/new`, `/helper`, `/chat/*`, `/listing/*` → GoatCounter absent | ID-5 |

---

### `tests/027_challenge_replay.js` (renamed: Wallet Trust Model)

> Content replaced in Sprint 5. Old test checked for `/wallet/challenge` (not implemented). New test verifies the actual payment-time trust model.

| Step/Check | Invariant(s) |
|-----------|-------------|
| `/wallet/register` returns session_token with no challenge/sig field (balance pre-check only) | IN-0 |
| Second wallet can register same address without ownership assertion | IN-0 |
| Register-only peer cannot open chat room (no payment) | IN-0, IN-3 |
| `/wallet/challenge` returns 404 — absent by design (architectural tripwire) | IN-0 |

---

### `tests/028_payment_edge_cases.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Underpayment (tx < invoice amount) → invoice stays pending | IN-2, IN-6 |
| Mempool API timeout → watcher does not crash; retries | IN-6 |
| API recovers → payment confirmed on next poll | IN-6 |

---

### `tests/029_ciphertext_only.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Canary plaintext sent through `nacl.box` pipeline | CH-1 |
| Sanity: peer decrypts canary back (proves real crypto, not no-op) | CH-1 |
| `encrypted_messages` columns contain no plaintext (raw/base64/hex) | CH-1 |
| Raw DB file + WAL + SHM contain no plaintext | CH-1 |

---

### `tests/030_content_type_spoofing.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Malformed JSON on `/wallet/register` → 4xx (not 500/panic) | SE-5 |
| Empty body on `/wallet/register` → 4xx (not 500/panic) | SE-5 |
| Missing `wallet_address` field → 400 | SE-5 |
| Body > 64 KB on non-chat endpoint → rejected (not 2xx) | SE-5 |
| GET on POST-only route → not 200 | SE-5 |
| Malformed JSON on auth-gated endpoint → 4xx (not 5xx) | SE-5 |

---

### `tests/031_concurrent_accept.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Two peers respond to same listing | RS-1 |
| Client fires two simultaneous accepts (Promise.all) | RS-1, RS-2 |
| Exactly one accept returns 200; other returns 409 | RS-1 |
| DB: exactly one response row with status='accepted' | RS-1 |

---

### `tests/032_concurrent_close.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Client and peer both close room simultaneously | CH-4, CH-5 |
| Room status → `closed` exactly once (not duplicated) | CH-4 |
| Second close returns 200 or 410, not 500 | CH-4 |
| No zombie `peer_left` / `closed` dual-state | CH-5 |

---

### `tests/033_devmode_prod_failsafe.js`

| Step/Check | Invariant(s) |
|-----------|-------------|
| Build binary without `-tags dev` | ID-6 |
| Start with `DEV_MODE=true` → fatal exit (non-zero) | ID-6 |
| Build binary with `-tags dev` | ID-6 |
| Start with `DEV_MODE=true` → starts successfully | ID-6 |

---

## Coverage Summary

| Invariant | Test(s) | Status |
|-----------|---------|--------|
| **ID-1** Plain address never in chats/listings/invoices | 001 (flow), 008 (registration) | ⚠️ No DB assertion |
| **ID-2** wallet_address_enc is AES-GCM ciphertext | encrypt_test.go | ⚠️ Unit only |
| **ID-3** wallet ownership proof = payment sender match (not registration challenge) | 027 T1-T4 (trust model), 035 T1/T4/T5 (payment verification) | ✅ Sprint 5 (model corrected) |
| **ID-4** Session tokens stored as SHA-256 hash | 008, 009 | ✅ |
| **ID-5** No IP in logs | 024 | ✅ Sprint 2 |
| **ID-6** WALLET_ENC_KEY required in prod | encrypt_test.go:TestPrepareEncKeyProd | ✅ |
| **SE-1** 401 without valid token | 009, 012, 013 | ✅ |
| **SE-2** Old token → 401 after refresh | 009 (explicit assertion) | ✅ |
| **SE-3** Client cannot call respond | 016 | ✅ Sprint 1 |
| **SE-4** Dev mode bypass blocked in prod | 020 | ✅ Sprint 2 |
| **SE-5** Rate limit 429, body limit 413 | 007, 005 | ✅ |
| **LS-1** Pending → active after payment | 001, 011 | ✅ |
| **LS-2** One active listing per client | 001, 006 | ✅ |
| **LS-3** Renewal blocked at 2 responses | 019 | ✅ Sprint 1 |
| **LS-4** Listing matched after chat opens | 001, 006 | ✅ |
| **RS-1** Max 2 pending responses | 017 | ✅ Sprint 1 |
| **RS-2** No duplicate respond | 001 | ✅ |
| **RS-3** 30-min cooldown after cancel | 021 | ✅ Sprint 2 |
| **RS-4** Region lock cross-city | 015 | ✅ Sprint 1 |
| **RS-5** Multi-slot balance scaling | 018 | ✅ Sprint 1 |
| **CH-1** Server never decrypts | 029 (DB column + raw file + WAL assertion) | ✅ Fable Five sprint |
| **CH-2** Only participants send/receive | 001, 010 | ⚠️ Poll path not tested |
| **CH-3** WS auth via header (not URL) | 010 | ✅ |
| **CH-4** Messages deleted on close | 001, 004 | ⚠️ No DB assertion |
| **CH-5** Cannot send to closed room | 004 | ✅ |
| **CH-6** Room scoped to listing_id | 002 | ✅ |
| **IN-1** payer_address stores HMAC hash | 013 | ⚠️ No DB column assertion |
| **IN-2** Invoice ownership check | 013 | ✅ |
| **IN-3** Sender hash match (incl. multi-input) | invoice_watcher_test.go (unit, DevMode=false) | ✅ |
| **IN-4** Double-confirm guard RowsAffected | `TestDoubleConfirmGuard` — txid + listing side-effect both checked | ⚠️ Chat side-effect structural only |
| **IN-5** Post-payment balance gate | `TestVerify_APIError_LeavesPending` (error path) + `TestBalanceThreshold_*` (4 math tests) | ✅ Sprint 1 |
| **IN-6** API error / grace window | `TestVerify_APIError_LeavesPending`, `TestGraceWindow_NotExpiredWithinGrace`, `TestGraceWindow_ExpiredAfterGrace` | ✅ |
| **RP-1** Review token single-use | 001, 003 | ✅ |
| **RP-2** Token only to client, ≥ 6h | 003 | ⚠️ No short-session test |
| **RP-3** Abuse report dedup | 012 | ✅ |
| **RP-4** Abuse ban thresholds + enforcement | 025, 036 | ✅ Thresholds SET correctly (Sprint 2) · ✅ Ban enforced in middleware (Sprint 6) · 036: 16/16 PASS |
| **WK-1** Message TTL cleanup | 022 | ✅ Sprint 2 |
| **WK-2** peer_left → listing restored | 011 | ✅ |
| **WK-3** wallet_sessions TTL cleanup | 023 | ✅ Sprint 2 |

**Totals after Sprint 6 (RP-4 ban enforcement):** ✅ 37 covered · ⚠️ 4 partial · ❌ 0 missing  
_(Sprint 1: 26✅ / 9⚠️ / 5❌ — Sprint 2: +5 — Fable Five: CH-1 ✅, ID-3 ✅ — Sprint 5: IN-0 ✅, CH-7 ✅, CH-8 ✅ — Sprint 6: RP-4 enforcement ✅)_  
E2E: **35/35 PASS** · Unit: **26/26 PASS**
