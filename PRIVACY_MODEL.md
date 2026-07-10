# Privacy Model — NA Room

## What NA Room Collects vs. Does Not Collect

| Data | Collected? | How stored |
|------|-----------|------------|
| Chat message content | Yes, transiently | AES nonce + ciphertext only; server cannot decrypt; deleted on close |
| Wallet address (active session) | Yes | AES-256-GCM encrypted (`wallet_address_enc`); decrypted only inside balance/payment workers |
| Wallet identity in listings, responses, chats | Derived value only | HMAC-SHA256 hash; plain address never stored in these tables |
| Invoice payer address | Derived value only | HMAC-SHA256 hash stored; plain address never stored in `invoices` table |
| Session token | Derived value only | SHA-256 hash stored; raw token issued to client once and never retained |
| Listing metadata (city, dep. type, urgency, language) | Yes | Plain text; required for board search |
| Invoice payment address | Yes | Plain BTC/LTC invoice address; on-chain transaction is public |
| IP addresses | No | Rate limiting uses hashed /24 subnet in memory; never written to disk |
| User accounts, email, phone, username | No | Not collected by design |
| Telegram identity | Yes, separately | Telegram `chat_id` stored in `client_listing_notifications` and `helper_board_subscriptions`; `helper_board_subscriptions` may also store `counselor_hash` (HMAC of helper wallet) when helper links Telegram — see Telegram Integration Privacy section |
| Pageview analytics | Optional, disabled by default | GoatCounter (self-reported, no cookies, no fingerprinting); only on public pages — see below |

---

## Wallet Verification Model (Two-Step)

NA Room uses a two-step model — not a single "wallet verified" claim:

**Step 1 — Balance pre-check (`POST /wallet/register`)**

`/wallet/register` checks that the submitted wallet address holds a balance at or above the platform threshold (≥$150 for clients, ≥$1000 for peers). It does NOT verify that the caller controls the address. There is no cryptographic signature, no challenge-response, no proof of key ownership. It is a public balance pre-check only.

There is no `/wallet/challenge` endpoint. Bitcoin/Litecoin message signing (challenge-response ownership proof at registration time) is intentionally not part of the architecture and is not planned. Wallet control is proven at payment time by on-chain sender verification — see Step 2 below.

After the pre-check passes, the server issues a session token keyed to the `HMAC-SHA256(HASH_KEY, address)` — the hash, not the plain address.

**Step 2 — Payment proof (invoice watcher, on-chain)**

Actual control of the wallet is established at payment time. When a payment to an invoice address is detected on-chain, the invoice watcher (`internal/worker/invoice_watcher.go`) performs `verifySenderAndBalance`:

1. **Sender hash match:** at least one transaction input address must hash to the same value as `invoices.payer_address` (which was set at invoice creation from the session's wallet hash). Hashes are compared — plain addresses never touch the DB comparison. Wrong sender → invoice immediately rejected.
2. **Post-payment balance check:** after the payment, the matched sender's balance is retrieved and checked against the post-payment threshold (listing: ≥$135; chat: ≥$975). Insufficient balance → invoice rejected.
3. **Underpayment:** transaction amounts below 99% of the invoice amount are ignored entirely — they do not trigger any action and do not block future valid payments.

**Chat room opens only when BOTH conditions pass.** No chat room is created until `verifySenderAndBalance` returns true AND `confirmInvoice` completes its transaction.

**Wrong sender → invoice rejected immediately.** If no input address in the payment transaction hashes to `invoices.payer_address`, the invoice is marked `rejected` before any balance check runs. The listing or chat is never activated.

**Single-transaction payment.** Each invoice is settled by one transaction. Multiple smaller transactions to the same invoice address are not aggregated — only a single transaction meeting the full amount threshold triggers the sender check and balance gate.

**Dev mode (`DEV_MODE=true` or `DEV_SKIP_PAYMENTS=true`):** both steps are bypassed. Invoices are auto-confirmed without blockchain checks. This is explicitly intended for development and testing only.

---

## Wallet Identity: HMAC-SHA256 Hashing

Every reference to a wallet in persistent tables (listings, responses, chat rooms, sessions, reputation, invoices) uses a keyed hash:

```
wallet_hash = HMAC-SHA256(HASH_KEY, "naroom:v1:" + normalize(wallet_address))
```

`HASH_KEY` is a dedicated environment variable for wallet hashing. If `HASH_KEY` is not set, the server falls back to `SERVER_SALT` for compatibility — but using a separate `HASH_KEY` in production is strongly recommended so that compromising one secret does not expose the other.

**Why this provides unlinkability without the key:**

HMAC-SHA256 is a one-way function keyed by `HASH_KEY`. An attacker who obtains the database but not `HASH_KEY` cannot reverse a hash to recover the wallet address, and cannot link two hashes to the same wallet even if they know the address. Without the key the hash values are opaque identifiers.

**With `HASH_KEY`:**

An attacker who has both the database and `HASH_KEY` can reverse hashes by computing `HMAC(key, candidate)` for any candidate address and comparing. This allows linking wallet addresses to listing and chat history. This is documented as the primary residual risk when the key is compromised alongside the database.

**Address normalization:** addresses are lowercased and trimmed before hashing to ensure `1AbC...` and `1abc...` produce the same hash.

---

## AES-256-GCM Encryption of Wallet Address at Rest

The `wallet_sessions` table requires the actual wallet address for blockchain API calls (balance checks, payment verification). To reduce the impact of a database-only leak, the address is stored encrypted:

```
wallet_address_enc = base64url(nonce || AES-256-GCM-Encrypt(WALLET_ENC_KEY, nonce, wallet_address))
```

- **Key:** `WALLET_ENC_KEY` (32-byte string from environment; required in production — server refuses to start without it)
- **Nonce:** 12 random bytes, unique per encryption, prepended to the ciphertext
- **Authentication tag:** GCM provides integrity; a tampered ciphertext returns an error rather than garbage plaintext

**When decryption happens:** only inside balance checker worker and invoice watcher worker, immediately before a blockchain API call. The plaintext is used inline and discarded. It is never written to logs, HTTP responses, or any other table.

**Dev mode:** if `DEV_MODE=true` and `WALLET_ENC_KEY` is absent, the server derives a stable key from `SERVER_SALT`. This fallback is explicitly blocked in production.

---

## End-to-End Encryption: Chat Messages

NA Room uses TweetNaCl (X25519 + XSalsa20-Poly1305) for chat messages.

**Key exchange:**

1. Each party (client and peer) generates an X25519 keypair in the browser.
2. The public key is registered with the server as part of the wallet/listing/response flow and stored in `responses.counselor_pubkey` and `invoices.client_pubkey`.
3. When a chat room is created, both public keys are available. Each side independently computes the shared secret: `sharedSecret = X25519(myPrivateKey, theirPublicKey)`.
4. Private keys never leave the browser. The server never sees them.

**Message storage:**

The server stores only:
- `nonce` — 24 random bytes chosen by the sender
- `ciphertext` — XSalsa20-Poly1305 encrypted payload

The server has no key material. It cannot decrypt stored messages. It relays ciphertext between WebSocket connections.

**Message deletion:**

Messages are deleted from `encrypted_messages` when **both** participants have closed the chat room (the second `POST /chat/{room_id}/close` triggers the deletion). If only one side has closed (status `peer_left` or `client_left`), messages remain intact so the other side can still read history. A TTL worker also deletes all messages older than 24 hours unconditionally, regardless of room status.

---

## Telegram Integration Privacy

Telegram integration is optional.

- `telegram_link_tokens` — one-time tokens that bind a Telegram `chat_id` to a listing or helper subscription. Token type `helper` also carries `counselor_hash` (HMAC-SHA256 of the helper's wallet address) to route direct chat-open notifications. Tokens are deleted by the TTL cleaner immediately after use or after a 10-minute expiry — no `counselor_hash` persists in this table beyond token consumption.
- `client_listing_notifications` — stores `(listing_id, telegram_chat_id)`. No wallet fields. TTL cleaner deactivates rows when the listing window expires (24h).
- `helper_board_subscriptions` — stores board notification filter preferences per `telegram_chat_id`. Also stores `counselor_hash` (HMAC-SHA256 of the helper's wallet; nullable) when the helper links Telegram. This enables the "chat opened" direct notification. TTL cleaner deactivates rows after the 24h subscription window.

**Privacy trade-off for `counselor_hash` in `helper_board_subscriptions`:**

Storing `counselor_hash` links a Telegram `chat_id` to a wallet-derived hash for the duration of the subscription. This is an intentional, opt-in association: helpers explicitly trigger the Telegram link. The column is nullable — subscriptions created before this feature was introduced have `counselor_hash = NULL`. The association is temporary (24h TTL, enforced by the TTL cleaner).

With `HASH_KEY`: an attacker who has both the database and `HASH_KEY` can compute `HMAC(HASH_KEY, candidate_address)` for any known address and check whether it matches a stored `counselor_hash` — linking a helper's wallet to their Telegram `chat_id`. This is documented as a residual risk below.

Without `HASH_KEY`: `counselor_hash` is an opaque HMAC value. It cannot be reversed to a wallet address.

A server operator who has live database access can directly observe the `counselor_hash → telegram_chat_id` mapping for active (non-expired, non-deactivated) helper subscriptions.

---

## Session Token Privacy

Wallet authentication issues a session token:

```
raw_token = 32 random bytes (crypto/rand)
token_hash = SHA-256(raw_token)
```

The `sessions` table stores only `token_hash`. The raw token is returned to the client once in the HTTP response and is never retained server-side.

For WebSocket authentication, the token is passed in the `Sec-WebSocket-Protocol` header rather than the URL query string, so it does not appear in server access logs.

---

## Database Attacker Scenarios

### Scenario A: Database stolen, `HASH_KEY` and `WALLET_ENC_KEY` unknown

| What the attacker has | What they can recover |
|----------------------|----------------------|
| `listings` rows | City, dep. type, urgency, language, timestamps — all plain. Wallet hashes present but unlinkable. |
| `responses` rows | Counselor public keys, timestamps. Wallet hashes unlinkable. |
| `chat_rooms` rows | Timestamps, status. Client/counselor hashes unlinkable. |
| `encrypted_messages` rows | Nonces and ciphertext only. Cannot decrypt. |
| `wallet_sessions` rows | `wallet_address_enc` — ciphertext only; cannot recover addresses without `WALLET_ENC_KEY`. |
| `sessions` rows | `token_hash` only; cannot recover raw tokens. |
| Telegram tables | `telegram_chat_id` values and listing/subscription links. `helper_board_subscriptions.counselor_hash` present but opaque without `HASH_KEY`. |

**Result:** Listing metadata and Telegram subscriptions are exposed. Wallet identity and chat content are protected. Helper `counselor_hash` values are present but unlinkable to wallet addresses without `HASH_KEY`.

### Scenario B: Database stolen + `HASH_KEY` known

| What changes |
|-------------|
| Wallet hashes in `listings`, `responses`, `chat_rooms`, `sessions`, `reputation` can be reversed by computing `HMAC(HASH_KEY, candidate)` for any candidate address. |
| An attacker can determine which listings and chat sessions are linked to a known wallet. |
| Chat message content is still E2E encrypted and cannot be read. |
| `wallet_address_enc` remains protected unless `WALLET_ENC_KEY` is also known. |

### Scenario C: Database stolen + both keys known

| What the attacker can recover |
|------------------------------|
| All wallet addresses associated with any listing, response, or session (historical). |
| Which wallet addresses were active at which times. |
| Full social graph: which client connected with which counselor. |
| Chat message content remains protected if messages were already deleted. |

---

## Residual Risks (Honest)

1. **Active wallet addresses.** `wallet_sessions.wallet_address_enc` is encrypted, but the key exists on the server. A live server compromise (access to running process, environment variables, or key material) exposes active wallet addresses. The mitigation is keeping session TTLs short and rotating `WALLET_ENC_KEY` periodically.

2. **Listing metadata.** City, dependency type, urgency, language, and timestamp are stored in plain text. In small communities or rare combinations, this metadata may narrow identity to a small set of people.

3. **Blockchain payment graph.** Invoice addresses are single-use but on-chain transactions are public. Anyone watching the blockchain can link an address to a payment on the platform.

4. **Third-party blockchain API visibility.** Balance checks and payment verification send wallet addresses to external APIs (mempool.space, BlockCypher). These providers can observe that the NA Room server is checking specific addresses. Running own nodes or routing through Tor mitigates this but is not implemented by default.

5. **Frontend delivery trust.** E2E encryption protects against server-side storage compromise but not against a malicious operator who ships altered JavaScript. Users who want to verify must inspect and build the frontend from source.

6. **Helper Telegram identity linkage.** When a helper links Telegram for notifications, their `counselor_hash` (HMAC-SHA256 of wallet address) is stored in `helper_board_subscriptions` alongside their Telegram `chat_id` for up to 24 hours. An attacker with both the database and `HASH_KEY` can link a helper's wallet to their Telegram identity for the duration of any active subscription. Mitigation: the subscription TTL is 24h and is enforced by the TTL cleaner; `counselor_hash` is nullable — helpers who do not link Telegram are unaffected; and without `HASH_KEY` the hash is opaque.

---

## Optional Analytics — GoatCounter

NA Room optionally supports [GoatCounter](https://www.goatcounter.com/) for privacy-first pageview analytics. It is **disabled by default** and must be explicitly opted in by the operator.

### What is tracked (if enabled)

Only anonymous pageviews on public, non-sensitive pages:

| Route | Tracked |
|-------|---------|
| `/` (landing) | Yes |
| `/how-it-works` | Yes |
| `/board/[city]` (public board) | Yes |
| `/new` (create listing) | **No** |
| `/listing/[id]` | **No** |
| `/chat/[room_id]` | **No** |
| `/helper` (wallet form) | **No** |
| Any payment or review flow | **No** |

The analytics script is injected by the browser only when the visitor is on an allowed public route. It is never loaded on routes that contain wallet, session, chat, listing-private, payment, or review state.

### What GoatCounter does NOT use

- No cookies
- No localStorage or sessionStorage
- No fingerprinting
- No session replay or heatmaps
- No ad pixels or third-party data sharing
- No cross-site tracking

GoatCounter collects: URL path, referrer, browser, screen size, country (from IP). IP addresses are not stored by GoatCounter. See [goatcounter.com/help/privacy](https://www.goatcounter.com/help/privacy).

### How to enable

Set `PUBLIC_GOATCOUNTER_CODE` to your GoatCounter subdomain in the frontend build environment, then rebuild the frontend:

```
PUBLIC_GOATCOUNTER_CODE=yourcode   # e.g. "naroom" for naroom.goatcounter.com
```

Leave it empty (the default) to disable analytics entirely. No script is loaded, no requests are made.

### How to disable

Leave `PUBLIC_GOATCOUNTER_CODE` unset or empty. This is the default. The analytics module checks the value at startup and does nothing if it is absent.
