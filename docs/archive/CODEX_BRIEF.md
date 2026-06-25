# NA Room — Security & Architecture Brief for Codex Review

## What we are building

NA Room (`naroom.net`) is an anonymous peer-to-peer support platform for people dealing with addiction. It connects people who need help (clients) with peers who have lived experience and want to support others.

The platform has zero professional care, zero accounts, zero identity. It is built entirely around the assumption that **the server must never be trusted** — by users or by regulators.

---

## Privacy philosophy

### For users

- No accounts. No email. No phone. No username.
- The only identifier is a Bitcoin or Litecoin wallet address — used solely to verify the person is real (balance check) and to authenticate sessions (wallet signature). The wallet is never linked to a real identity.
- All chat messages are end-to-end encrypted (X25519 + XSalsa20-Poly1305 via TweetNaCl). The server stores only `nonce + ciphertext`. It cannot decrypt anything.
- All messages are permanently deleted when a session closes.
- Wallet addresses are **never stored in plain text** in listings, responses, invoices, or chat rooms — only `HMAC-SHA256(server_salt, address)` hashes. If the database leaks without the salt, user activity cannot be linked to any wallet.
- The platform works over Tor (.onion). Tor Browser with no language headers defaults to English — no fingerprinting attempt.
- No IP logging beyond the minimum required for rate limiting (hashed /24 subnet, never persisted).
- Open source: the full codebase will be published on GitHub so anyone can verify encryption, data deletion, and absence of hidden functions.

### For the platform creator

- The server must not know who the users are, even under legal pressure.
- No logs that link IP to activity.
- No third-party analytics, no CDN that sees plaintext traffic.
- Production deployment on a privacy-respecting VPS in a jurisdiction outside US/EU.
- Tor hidden service as the primary access method — clearnet domain as secondary.
- The server salt (`SERVER_SALT`) is the only secret that can link hashed wallet addresses to real addresses. It must be stored only in environment variables, never in the database or source code.
- Because all chat content is E2E encrypted and deleted on close, there is nothing to hand over even if the server is seized.

---

## Tech stack

- **Backend:** Go 1.25, chi router, SQLite (WAL mode, `MaxOpenConns=1`), nhooyr.io/websocket
- **Frontend:** SvelteKit 5 (runes), TweetNaCl for E2E encryption in browser
- **Crypto payments:** Bitcoin (mempool.space API) + Litecoin (BlockCypher API), HD wallet (BIP-32 xpub) for address generation
- **E2E encryption:** X25519 key exchange + XSalsa20-Poly1305 (NaCl box) — ephemeral keypairs per session, server never sees plaintext
- **Auth:** `POST /wallet/register` with balance check → session token (32 random bytes, only SHA-256 hash stored in DB). No wallet signature required — payment-from-registered-address serves as ownership proof.
- **Languages:** EN / RU / ES / KA (Georgian)
- **E2E test suite:** 25 test suites (001–025), all passing

---

## What is already implemented

### Core flow
- Full E2E flow: post listing ($5) → peer responds (free) → client accepts → peer pays $15 → E2E encrypted chat opens → session closes → all messages deleted
- WebSocket chat with message history (delivered on reconnect)
- Tor fallback: HTTP long-polling (`/chat/poll/send` + `/chat/poll/receive`) for Tor Browser where WebSocket may be blocked

### Wallet verification and payment-as-ownership-proof

This is a key design decision: **we do not use cryptographic wallet signing for payment verification.** Instead, we use payment-from-registered-address as proof of wallet ownership:

1. User calls `POST /wallet/register` with their wallet address.
2. Server looks up the wallet's balance on-chain and verifies it meets the minimum threshold.
3. Server stores `HMAC-SHA256(salt, address)` (never the plain address) in `wallet_sessions`.
4. When user creates a listing or accepts a response, an invoice (unique per-action HD wallet address) is generated. The `payer_address` field in the invoice stores the HMAC hash of the user's registered wallet — not the plain address.
5. `InvoiceWatcher` polls for confirmed payments. When a payment arrives, it reads all sender addresses from all transaction inputs (Bitcoin and Litecoin transactions can have multiple inputs from different UTXO addresses). For each sender address, it computes `HMAC-SHA256(salt, sender)` and checks if any of them matches the stored `payer_address` hash.
6. If no match — invoice is rejected. The payment came from a wallet that didn't register.
7. If match — we additionally check that the sender's balance (after the payment) still meets the minimum threshold, with a buffer for price volatility and invoice cost.

**Why not cryptographic wallet signing?** Standard wallets (Exodus, Trust Wallet, Ledger, hardware wallets) do not expose `signMessage()` in their UIs for arbitrary messages. The only signing they guarantee is for transactions. So the blockchain transaction itself is the proof of ownership.

**Why HMAC-SHA256 and not SHA256(salt + address)?** Length-extension attack resistance. Using `HMAC(key, data)` is correct; `SHA256(salt || data)` is vulnerable to length extension. All hashes in the system use `crypto/hmac` + `crypto/sha256`.

**Important note on UTXO multi-input transactions:** A single Bitcoin or Litecoin transaction can spend inputs from multiple different addresses (e.g. a wallet consolidating UTXOs). The payer verification checks ALL input addresses, not just the first one.

### Balance gate logic

- **Client** (listing creation): must hold ≥ $150 to post. After paying $5 invoice, the post-payment balance check requires ≥ $135 ($150 − $5 invoice − $10 volatility buffer).
- **Peer** (chat acceptance): must hold ≥ $1000 to accept a case. After paying $15 invoice, the post-payment balance check requires ≥ $975 ($1000 − $15 invoice − $10 volatility buffer).
- **Per-slot scaling**: a peer with $1000 can hold 1 active response slot; each additional slot requires an additional $1000.
- **`balance_status`** in `wallet_sessions` is updated by `BalanceChecker` worker every 10 min. If balance drops below threshold, existing chats close and listings are hidden.

### Region lock

Peers are locked to a single city after their first response. This prevents a single peer from monopolizing multiple cities.

- `reputation.region` starts as `''` (empty string, set when peer first registers).
- When a peer submits their first response, the transaction atomically sets `region` via `UPDATE reputation SET region = ? WHERE counselor_hash = ? AND region = ''`.
- After the UPDATE, the transaction reads back the actual `region` value. If it differs from the listing's city (which would indicate a race with a concurrent transaction), the response is rejected with `{"error": "region_locked", "locked_region": "city"}`.
- Subsequent responses must match the locked region. No exceptions, no reset mechanism.
- Frontend: before submitting a response, the UI calls `GET /peer/region` and shows a warning modal if the peer has never responded before (so they understand they're locking their region).

### Security

- **Wallet signature verification** — BTC and LTC legacy message signing (P2PKH + P2WPKH). Covers Electrum, BlueWallet, Ledger. 7/7 unit tests pass.
- **Server-issued nonce (challenge)** — prevents replay attacks. Challenge TTL 5 min, single-use.
- **Session tokens** — 32 random bytes, only HMAC-SHA256 hash stored in DB, 24h TTL, rotation + revocation endpoints.
- **WebSocket auth via `Sec-WebSocket-Protocol`** — token passed as WS subprotocol (browser cannot send custom headers in WS upgrade). URL is clean, no token in query string.
- **Rate limiting** — per hashed IP subnet (/24), in-memory token bucket, no IP persisted. In dev mode, rate limits are bypassed so E2E tests aren't throttled.
- **Wallet address hashing** — `listings.wallet_hash`, `responses.counselor_hash`, `chat_rooms.client_hash/counselor_hash`, `invoices.payer_address` all store `HMAC-SHA256(salt, address)`. Plain address is stored AES-256-GCM encrypted in `wallet_sessions.wallet_address_enc` (needed for blockchain API calls). `wallet_challenges` table has been dropped (Sprint 1).
- **Hard fail on missing encrypted address** — if decryption of `wallet_sessions.wallet_address_enc` fails when creating an invoice, the handler returns 500 and aborts. No silent fallback.
- **Reputation system** — `counselor_hash` keyed, anonymous thumbs up/down, one-time review tokens.
- **Abuse reporting** — peer reports client via room participation proof (no moderation, reputation-based).
- **TTL cleaner** — expired sessions, challenges, messages, and peer_left rooms cleaned automatically.
- **Body size limits** — 64KB for JSON, 8MB for images.
- **No IP logging** — middleware strips IPs before logging.
- **Dust payment guard** — 1% tolerance on invoice amount to handle mempool fee fluctuation.
- **Double-confirm guard** — `UPDATE invoices SET status='confirmed' WHERE id=? AND status='pending'` with `RowsAffected()` check prevents concurrent workers from processing the same invoice twice.
- **MaxOpenConns=1 deadlock prevention** — all queries inside a transaction use `tx.QueryRow`/`tx.Exec`, not `h.DB.QueryRow`. External calls (price API, address generation, wallet address lookup) happen before `BEGIN`.

### Invoice watcher error handling

Three outcomes when a payment is detected:

| Situation | Action |
|-----------|--------|
| Sender hash doesn't match payer hash | Reject invoice (`status = 'rejected'`) |
| No sender addresses in transaction | Reject invoice |
| Empty `payer_address` in invoice | Reject invoice (data integrity error) |
| Blockchain API error (balance check) | Leave pending — retry next cycle |
| Price feed unavailable | Leave pending — retry next cycle |
| Balance sufficient + sender matches | Confirm invoice |

Rejecting on API errors was explicitly avoided: a temporary mempool.space or BlockCypher outage should not permanently reject a valid payment.

### Database schema (key tables)
```
listings         — wallet_hash (HMAC, not address), city, dep_type, help_type, urgency, languages
responses        — counselor_hash (HMAC, not address), counselor_pubkey, listing_id
chat_rooms       — client_hash, counselor_hash, client_pubkey, counselor_pubkey, expires_at
encrypted_messages — sender_pubkey, nonce, ciphertext (server cannot decrypt)
wallet_sessions  — wallet_address_enc (AES-256-GCM encrypted, needed for blockchain API), wallet_hash, balance_status
sessions         — token_hash (HMAC-SHA256 of raw token), wallet_hash, role
reputation       — counselor_hash, region (city lock), sessions_completed, thumbs_up, thumbs_down
invoices         — payer_address (HMAC hash, not plain address), type, currency, status
```

### Frontend
- Board, listing creation, listing view with peer responses, chat with E2E encryption
- Region lock UI: `GET /peer/region` called before submitting first response; warning modal if unlocked; blocked message if locked to different city
- i18n: 4 languages (EN/RU/ES/KA), auto-detection (localStorage → navigator.language → English fallback)
- How-it-works page with role switcher (client / peer), FAQ
- JSON-LD FAQPage schema for search engines
- `llms.txt` for AI agent indexing
- `sitemap.xml`, `robots.txt`

---

## What is planned / not yet done

### Security gaps remaining

1. **`wallet_sessions.wallet_address_enc`** — AES-256-GCM encrypted at rest ✅ (Sprint 1). Plain address only decrypted inside balance/payment workers. `wallet_challenges` table dropped (was orphaned, stored plain address). `reconnection_hashes` column removed (was dead code).

2. **BIP-322** — new Bitcoin message signing format not implemented. Current coverage (legacy P2PKH + P2WPKH) covers all major wallets in practice. Needs real-world wallet testing before implementation.

### Production deployment (not done)
- Choose privacy-respecting VPS provider (Njalla / BuyVM / 1984 Hosting / OrangeWebsite)
- Set up Tor hidden service (`.onion` address)
- Configure `SERVER_SALT`, `BTC_XPUB`, `LTC_XPUB` as environment secrets
- TLS for clearnet (Let's Encrypt or manual)
- systemd unit for process supervision
- Log rotation with no sensitive data
- Backup strategy for SQLite WAL (without leaking salt)

### Open source GitHub release
- Publish full source code to `github.com/naroom`
- Write/update `SECURITY.md` explaining threat model
- Write `SELF_HOSTING.md` for operators
- Make domain configurable via env (no hardcoded `naroom.net`)

---

## What we want Codex to review this time

We have made significant changes since the first review. Please focus on:

1. **Payer verification logic** (`internal/worker/invoice_watcher.go`, `verifySenderAndBalance`) — does the multi-input check (`senders []string`, iterate and hash-match) correctly close the bypass? Are there edge cases with UTXO consolidation or CoinJoin transactions we haven't considered?

2. **Balance gate arithmetic** — the formula is `minHold - invoiceCost - $10`. Is the $10 volatility buffer sufficient for BTC/LTC price swings in a 30-second polling interval? Is there a risk of a legitimate user failing the gate due to confirmation timing?

3. **Region lock atomicity** — `UPDATE reputation SET region = ? WHERE counselor_hash = ? AND region = ''` followed by `SELECT region`. SQLite with `MaxOpenConns=1` serializes all writes, but is there any scenario where this UPDATE succeeds but the region ends up wrong?

4. **Hard fail on decryption error** — if `crypto.DecryptAddress(WALLET_ENC_KEY, wallet_address_enc)` fails when creating an invoice (corrupt ciphertext, wrong key), the handler returns 500 and aborts. Is there any path where a legitimately registered wallet could have a corrupt `wallet_address_enc` value, or is it safe to always treat decryption failure as a fatal error?

5. **API error handling in invoice_watcher** — returning `false` (leave pending) on balance/price API errors means an invoice stays pending indefinitely if the API is down for > 1 hour (invoice expires). Is this the right trade-off, or should we extend invoice TTL when API errors are detected?

6. ~~**`wallet_sessions.wallet_address` plain text**~~ — RESOLVED (Sprint 1). `wallet_sessions.wallet_address_enc` stores AES-256-GCM ciphertext; plain address only decrypted transiently for blockchain API calls. Residual risk: attacker with both DB and `WALLET_ENC_KEY` can recover active session addresses. See `THREAT_MODEL.md`.

7. **Overall threat model** — given the current implementation, what does a server operator actually know about users? What does an attacker with database + salt know? What is the residual risk we have not yet eliminated?

---

## Key files for review

```
naroom/
  cmd/naroom/main.go                — server entry point, routing, worker init
  internal/db/schema.sql            — full database schema
  internal/handler/
    wallet.go                       — wallet verify, session issue, upsertWalletSession
    listing.go                      — create listing (payer hash stored, hard fail on empty)
    respond.go                      — peer responds (region lock, atomic UPDATE WHERE region='')
    accept.go                       — client accepts (counselor payer hash, hard fail on empty)
    chat_ws.go                      — WebSocket chat, CloseChat
    invoice.go                      — invoice status
  internal/worker/
    invoice_watcher.go              — payment confirmation, multi-input sender check, verifySenderAndBalance
    balance_checker.go              — periodic balance polling
  internal/crypto/
    verify.go                       — BTC/LTC signature verification
    hash.go                         — WalletHash (HMAC-SHA256)
    mempool.go                      — mempool.space API (BTC), FindPayment returns []string senders
    blockcypher.go                  — BlockCypher API (LTC), FindPayment returns []string senders
```

---

## Threat model summary

**What the server operator knows:**
- That a listing was posted in city X for dependency type Y at time Z
- That a peer responded and a chat was opened
- Approximate session duration
- **Active wallet addresses (encrypted)** — `wallet_sessions.wallet_address_enc` stores AES-256-GCM ciphertext; `wallet_challenges` table dropped (Sprint 1)

**What the server operator does NOT know:**
- Content of any messages (E2E encrypted, deleted on close)
- Historical wallet identity in listings/chats/responses (HMAC-hashed)
- Any personally identifying information

**What an attacker who reads the database WITHOUT salt knows:**
- Listing metadata (city, dependency type, time)
- **AES-GCM ciphertext from `wallet_sessions.wallet_address_enc`** — cannot decrypt without `WALLET_ENC_KEY`
- No message content

**What an attacker who reads the database WITH salt knows:**
- Everything above, plus: can reverse HMAC hashes in listings/chats/responses → real wallet addresses
- Full picture of who participated in what listing and when
- Still cannot read message content (E2E encrypted, already deleted)

**Residual risk (Sprint 1 status):**
- `wallet_sessions.wallet_address_enc` is AES-256-GCM encrypted — DB reader without `WALLET_ENC_KEY` cannot recover addresses ✅
- `wallet_challenges` table dropped entirely ✅
- Remaining: if attacker has both DB + `WALLET_ENC_KEY`, they can decrypt. Key must be protected separately from DB (environment variable, secret manager).
