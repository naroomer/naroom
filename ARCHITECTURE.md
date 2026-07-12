# Architecture — NA Room

## System Overview

```
  Browser (SvelteKit)
       |
       |  HTTPS / WSS
       v
  Go backend (chi router)
       |
       |-- SQLite (WAL, single file)
       |-- BTC/LTC blockchain APIs (mempool.space, BlockCypher)
       |-- Telegram Bot API (optional)
```

The backend is a single Go binary. There is no external cache, no message broker, and no separate auth service. SQLite runs in WAL mode with `MaxOpenConns=1` to prevent concurrent write contention.

---

## Component List

### Backend (Go 1.25, `cmd/naroom/main.go`)

- **Router:** `github.com/go-chi/chi/v5`
- **WebSocket:** `nhooyr.io/websocket`
- **Database driver:** `modernc.org/sqlite` (pure-Go, no CGO)
- **Bitcoin/Litecoin:** `github.com/btcsuite/btcd` for signature verification and HD wallet derivation
- **HTTP handlers:** `internal/handler/`
- **Middleware:** `internal/middleware/`
- **Background workers:** `internal/worker/`
- **Telegram bot client:** `internal/telegram/`

### Frontend (SvelteKit 5, `/frontend`)

- SvelteKit 5 with TypeScript
- TweetNaCl (`tweetnacl`) for X25519 key generation and XSalsa20-Poly1305 encryption/decryption in the browser
- Communicates with backend via REST and WebSocket

### Database (SQLite)

- Single `.db` file, WAL journal mode, `synchronous=NORMAL`
- All migrations run on startup (`internal/db/db.go`)
- Schema: `internal/db/schema.sql` (embedded via `//go:embed`)

### Telegram Bots (optional)

- Client bot: notifies a client's Telegram when a peer responds to their listing
- Helper bot: delivers board listings to peers who subscribe by filter
- No wallet identity is stored in or linked from the Telegram tables

---

## Request Flow

### 1. Wallet Registration

```
POST /wallet/register
  Body: { wallet_address, currency, role, signature, challenge_nonce }
  |
  +--> Verify BTC/LTC signature (internal/crypto/verify.go)
  +--> HMAC-SHA256(HASH_KEY, address) => wallet_hash
  +--> AES-256-GCM encrypt address => wallet_address_enc
  +--> Upsert wallet_sessions row (wallet_hash, wallet_address_enc, role, ...)
  +--> Generate 32-byte random token; store SHA-256(token) in sessions table
  +--> Return: { session_token, role }
```

### 2. Listing Creation

```
POST /listing/create  [RequireSession middleware]
  |
  +--> Decrypt wallet_address_enc => address (in-memory)
  +--> HMAC(address) => wallet_hash
  +--> Check: no existing active/pending listing for this wallet_hash (409 if exists)
  +--> INSERT INTO listings (status='pending', wallet_hash, city, ...)
  +--> Derive next HD wallet address for BTC or LTC (invoice_index table)
  +--> INSERT INTO invoices (type='listing', address=hd_address, ...)
  +--> Return: { listing_id, invoice_id, invoice_address, amount_usd }
```

### 3. Peer Response

```
POST /listing/{id}/respond  [RequireSession, role=peer]
  |
  +--> Role check: 403 if role != "peer"
  +--> Balance gate: active_responses * $1000 < min_required_usd (else 403)
  +--> Region lock: UPDATE reputation SET region=? WHERE region='' (atomic)
  +--> Check: no duplicate response by same counselor_hash (409)
  +--> Check: listing has < 2 pending responses (409)
  +--> INSERT INTO responses (counselor_hash, counselor_pubkey, status='pending')
  +--> Return: { response_id }
```

### 4. Invoice Confirmation (background worker)

```
invoice_watcher (polls blockchain every ~30s)
  |
  +--> GET /tx/:txid from mempool.space or BlockCypher
  +--> Verify sender: HMAC all input addresses, compare to invoices.payer_address
  +--> Verify balance: sender_balance >= (minHold - invoice_cost - $10 buffer)
       Uses max(price_at_creation, current_price) to favor user
  +--> RowsAffected guard: UPDATE ... WHERE status='pending' (idempotent)
  |
  type='listing':
    +--> UPDATE listings SET status='active', visible_until=now+ListingTTL (default 24h)
  |
  type='chat':
    +--> INSERT INTO chat_rooms (client_hash, counselor_hash, client_pubkey, counselor_pubkey)
    +--> UPDATE listings SET opened_chats_count = opened_chats_count + 1
         (listing stays status='active' while opened_chats_count < 2; closes permanently at 2)
    +--> UPDATE invoices SET chat_room_id=? (prevents duplicate room creation)
```

### 5. Chat Room (WebSocket)

```
GET /ws/chat/{room_id}
  Sec-WebSocket-Protocol: naroom-token.<base64(session_token)>
  |
  +--> Extract token from protocol header (never URL)
  +--> Validate session token (SHA-256 lookup in sessions table)
  +--> Verify wallet_hash matches client_hash or counselor_hash in chat_rooms
  +--> Upgrade to WebSocket
  +--> Hub pattern: in-memory map[room_id][]*Conn
  |
  On message receive:
    +--> Store { nonce, ciphertext, sender_pubkey, msg_type } in encrypted_messages
    +--> Broadcast to all room connections
  |
  On close (client closes):
    +--> DELETE FROM encrypted_messages WHERE room_id=?
    +--> UPDATE chat_rooms SET status='closed', closed_by='client'
    +--> Issue review_token if session >= 6h
```

---

## E2E Encryption Flow

```
Browser A (client)             Server                Browser B (peer)
     |                           |                        |
     | Generate X25519 keypair   |                        |
     | pubkey_A sent in request  |                        | Generate X25519 keypair
     |                           |                        | pubkey_B sent in respond
     |                           | Store pubkey_A,        |
     |                           | pubkey_B in DB         |
     |                           |                        |
     |        Both browsers receive pubkey of other party (from room join response)
     |                                                    |
     | sharedSecret = X25519(privA, pubB)                 | sharedSecret = X25519(privB, pubA)
     |                    (same value)                    |
     |                                                    |
     | nonce = random 24 bytes                            |
     | ciphertext = XSalsa20-Poly1305(sharedSecret, nonce, plaintext)
     |                                                    |
     |------ { nonce, ciphertext } -----> Server -------> |
     |                           |                        |
     |                    Store nonce+ciphertext          |
     |                    (cannot decrypt)                |
     |                                                    |
     |                                    plaintext = decrypt(sharedSecret, nonce, ciphertext)
```

Key bundle: the peer's `counselor_pubkey` is stored in the `responses` table when they respond. The client's `client_pubkey` is stored in the `invoices` table when the chat invoice is created (from the accept response step). Both are available to both parties in the room join response.

---

## HD Wallet and Invoice Address Generation

Each payment invoice gets a unique, never-reused BTC or LTC address derived from the operator's extended public key (xpub/zpub/Ltub):

```
BTC: xpub (P2PKH) or zpub (P2WPKH)
LTC: Ltub (P2PKH)

Derivation: m/0/<index>
  index = next_index from invoice_index table (atomically incremented)
```

Addresses are derived using `btcd/btcutil/hdkeychain` without access to private keys. The operator's private key is never on the server.

`price_at_creation` is set at invoice creation time and used at confirmation if it yields a more favorable exchange rate for the user than the current rate.

---

## Key Security Invariants by Layer

### Middleware layer (`internal/middleware/`)

| Middleware | Invariant enforced |
|-----------|-------------------|
| `RequireSession` | 401 on missing/invalid/revoked/expired token (SE-1) |
| `NoLogIP` | IP address never written to logs (ID-5) |
| `ByIP` (rate limit) | 429 after per-endpoint burst; uses hashed /24 subnet key (SE-5) |
| `LimitBody` | 413 for oversized bodies (SE-5) |
| Security headers | HSTS, X-Frame-Options, CSP, referrer policy set on all responses |

### Handler layer (`internal/handler/`)

| Handler | Invariants enforced |
|---------|-------------------|
| `wallet.go` | Encrypt address before INSERT; HMAC before storing in sessions (ID-1, ID-2, ID-4) |
| `listing.go` | Wallet hash only in listings; duplicate listing check (LS-1, LS-2) |
| `respond.go` | Role check before DevMode; balance gate; region lock; response cap (SE-3, RS-1–RS-5) |
| `accept.go` | Invoice created with HMAC payer_address (IN-1) |
| `invoice.go` | Ownership check: only session owner sees invoice (IN-2) |
| `chat_ws.go` | Token from protocol header; participant check; WS relay only (CH-2, CH-3) |
| `review.go` | Token single-use; issued only to client on clean close ≥ 6h (RP-1, RP-2) |
| `abuse.go` | Role=peer required; dedup by pair hash (RP-3, SE-3) |

### Worker layer (`internal/worker/`)

| Worker | Function |
|--------|---------|
| `invoice_watcher.go` | Polls blockchain, verifies sender hash, checks balance gate, confirms invoice (IN-3–IN-6) |
| `ttl_cleaner.go` | Deletes messages >24h, closes peer_left rooms, prunes expired sessions and wallet_sessions (WK-1–WK-3) |
| `balance_checker.go` | Periodically refreshes balance_usd in wallet_sessions for active peers |

---

## Rate Limiting

Rate limiting is applied per hashed /24 subnet using a token bucket algorithm (`golang.org/x/time/rate`):

```
key = SHA-256(RemoteAddr[:last_octet_cut])  // /24 subnet, hashed
```

Each endpoint has its own burst and refill rate. Example limits:

- `POST /wallet/register`: burst 10, rate 1/min
- `POST /listing/create`: burst 5, rate 1/5min
- `POST /listing/{id}/respond`: burst 10, rate 2/min
- Chat WebSocket: burst 20 messages, rate 1/s

When burst is exceeded the server returns `429 Too Many Requests`. The subnet key is kept in memory and never written to disk.

---

## WebSocket Design

The chat WebSocket follows the `nhooyr.io/websocket` library pattern:

- **Auth:** token extracted from `Sec-WebSocket-Protocol` header value (`naroom-token.<token>`). The token is never placed in the URL to prevent log leakage.
- **Hub:** in-memory `sync.Map` keyed by room ID. Each connected client registers a channel.
- **Relay:** server receives a message from one connection, stores nonce+ciphertext in `encrypted_messages`, and fans out to all other connections in the same room.
- **Close:** the client who initiated the close triggers message deletion and status update. The peer receives a close frame and the room status transitions to `closed`.
- **Fallback (poll):** `GET /chat/{room_id}/poll` and `POST /chat/{room_id}/send` provide HTTP long-poll for clients that cannot maintain a WebSocket connection.
