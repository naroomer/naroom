# Testing Guide — NA Room

## Test Types

| Type | Location | Runner | Count |
|------|----------|--------|-------|
| Go unit tests | `internal/crypto/`, `internal/worker/` | `go test` | 26 |
| E2E tests | `e2e/tests/` | Node.js | 25 |
| Frontend type check | `frontend/` | `npm run check` | — |

Current status: **25/25 E2E · 26/26 unit — all green**

---

## Running the Full Suite

```bash
bash scripts/selftest.sh
```

This script:
1. Builds the Go binary (`go build ./...`)
2. Runs Go unit tests (`go test ./...`)
3. Runs `npm run check` in `/frontend`
4. Starts the E2E runner (`node e2e/run_all.js`)

All steps must pass. The script exits non-zero on any failure.

---

## Running Individual E2E Tests

```bash
node e2e/tests/001_happy_path.js
```

Each test file is a standalone module that boots its own server instance. Tests do not share state.

To run a subset, pass file paths directly to the runner:

```bash
node e2e/run_all.js e2e/tests/007_rate_limiting.js e2e/tests/018_balance_threshold.js
```

---

## E2E Test List

| File | What it covers |
|------|---------------|
| `001_happy_path.js` | Full flow: wallet register → listing create → peer respond → accept → invoice confirm → chat open → send/receive encrypted message → close → review token |
| `002_stale_room_guard.js` | Peer with an old active room must not receive a stale room when a new listing starts |
| `003_role_separation_review.js` | Review token issued only when client closes; peer close yields no token; token is single-use (409 on reuse) |
| `004_remote_close_state.js` | When client closes room, peer detects terminal state via WebSocket and cannot send further messages |
| `005_large_image_payload.js` | Image payload within 8 MB accepted; non-chat JSON body over 64 KB returns 413 |
| `006_state_bleed.js` | Two independent flows sharing a database see only their own data; close of one flow does not affect the other |
| `007_rate_limiting.js` | Burst of requests to `/wallet/register` returns 429 after burst limit exceeded (runs with `devMode=false`) |
| `008_wallet_challenge.js` | Wallet registration: valid registration returns session token; re-registration updates session; missing field returns 400; invalid signature returns 401 |
| `009_session_lifecycle.js` | Session token issue, authenticate, refresh (old token becomes 401), revoke (revoked token becomes 401) |
| `010_ws_auth.js` | WebSocket auth via `Sec-WebSocket-Protocol` header; no token → rejected; invalid token → rejected; URL contains no token |
| `011_peer_left_expiry.js` | Peer closes WebSocket → room enters `peer_left`; after TTL, room closes and listing is restored to active; no review token issued |
| `012_abuse_report.js` | Abuse report requires prior chat room participation (403 without); duplicate report from same pair returns 409 |
| `013_invoice_scoping.js` | Invoice status requires session (401 without); non-owner session returns 403; owner returns 200 |
| `014_reputation.js` | Fresh peer reputation row has `sessions_completed=0`; completed session increments it; board shows peer with region and stats; thumbs up/down recorded |
| `015_region_lock.js` | Peer responds in tbilisi → region locked; subsequent response attempt in batumi returns 403 with `locked_region=tbilisi` |
| `016_role_separation_respond.js` | Client role cannot call respond endpoint (403); listing owner with client role also rejected; listing remains active after rejected attempts |
| `017_max_responses.js` | Two peers respond successfully (slots 1 and 2); third peer returns 409; database asserts exactly 2 pending rows |
| `018_balance_threshold.js` | Balance gate enforced in `devMode=false`: peer at $1000 fills slot 1 (OK), fails slot 2 (403); after raising to $2000, slot 2 succeeds |
| `019_renewal_blocked.js` | Renewal allowed at 0 and 1 pending responses; blocked (409) at 2 pending responses; `GET /listing/{id}` shows `can_renew=false` |
| `020_devmode_headers.js` | In `devMode=false`, `X-Dev-Wallet` and `X-Dev-Role` headers are ignored and do not grant access |
| `021_cancel_cooldown.js` | Peer who cancels a response cannot respond to the same listing for 30 minutes (cooldown_until enforced) |
| `022_message_ttl.js` | TTL worker deletes encrypted messages older than 24 hours from the database |
| `023_wallet_session_ttl.js` | `wallet_sessions` row is pruned when all auth sessions for that wallet have expired |
| `024_log_privacy.js` | Server log output during a full flow contains no raw IP address, wallet address, or session token |
| `025_abuse_ban.js` | Three abuse reports against a client sets `banned_until = now + 72h`; five reports sets a long-term ban |

---

## How TestServer Works

`e2e/lib/server.js` exports `TestServer`, which:

1. Creates a temporary SQLite database in `os.tmpdir()`.
2. Generates random test values for `SERVER_SALT`, `WALLET_ENC_KEY`, and `BTC_XPUB` / `LTC_XPUB`.
3. Spawns the Go backend binary on a random available TCP port using `child_process.spawn`.
4. Polls `GET /healthz` until the server responds (up to 10 seconds).
5. Exposes `srv.base` (the `http://localhost:<port>` URL) for test use.
6. On `srv.stop()`, sends SIGTERM to the child process and deletes the temp database file.

Each test constructs its own `TestServer` instance. Tests are fully isolated.

---

## How `registerDirect()` Works

Most E2E tests call `api.verifyWallet(address, currency, role)` which hits `POST /wallet/register`. In `devMode=true` (the default for the test suite), the server accepts a direct registration payload that bypasses the BIP-322 / message-signing challenge. This allows tests to register arbitrary wallet addresses without needing real private keys.

For tests that specifically require `devMode=false` (tests 007, 018, 020), the `TestServer` is instantiated with `{ devMode: false }`. These tests use the dev-mode bypass path for registration only when the endpoint still accepts it (007, 020 test that it does not; 018 injects balance via a test hook).

---

## Unit Test Coverage

### `internal/crypto/verify_test.go` (10 tests)

Tests for HMAC determinism, address normalization, BTC/LTC signature verification (P2PKH, P2WPKH), wrong-signature rejection, wrong-address rejection, replay salt rotation, and challenge expiry.

### `internal/crypto/encrypt_test.go` (7 tests)

Tests for AES-256-GCM round-trip, wrong-key error, tamper detection (GCM auth tag), unique nonce per call, dev-mode key derivation, prod-mode missing key fatal, and truncated input error.

### `internal/worker/invoice_watcher_test.go` (9 tests)

Tests for empty payer address rejection, no-senders rejection, wrong-wallet rejection, multi-input match, API error leaving invoice pending, double-confirm guard, grace window (still pending), grace window (expired), and balance threshold math at exact pass/fail boundaries for listing and chat types.

---

## How to Add a New Test

1. Create `e2e/tests/NNN_description.js` following the pattern of an existing test.
2. Import `TestServer`, `ApiClient`, and assertion helpers from `e2e/lib/`.
3. Export an `async function run()`.
4. Add it to `e2e/run_all.js` (or it will be picked up automatically if the runner globs the directory).
5. Update `docs/INVARIANTS.md` with the invariant the test covers.
6. Update `docs/TEST_MATRIX.md` to add the new test row.

---

## Cross-References

- [docs/INVARIANTS.md](docs/INVARIANTS.md) — full list of security invariants with enforcement locations and coverage status
- [docs/TEST_MATRIX.md](docs/TEST_MATRIX.md) — map from each invariant to the test(s) that prove it
- [docs/E2E_PROTOCOL.md](docs/E2E_PROTOCOL.md) — protocol for writing and reviewing E2E tests
