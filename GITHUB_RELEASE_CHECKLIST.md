# GitHub Release Checklist — NA Room

Use this checklist before tagging a release or merging to the main branch. Work through each section top to bottom. All items must pass.

---

## 1. Pre-Release Security Scan

Check that no sensitive files or credentials are tracked in git.

- [ ] `git status` shows no `.env` files staged or modified
- [ ] `git ls-files | grep -E '\.env$|\.env\.'` returns empty
- [ ] `git ls-files | grep -E '\.db$|\.db-wal$|\.db-shm$'` returns empty (no SQLite databases committed)
- [ ] `git ls-files | grep -iE 'private|secret|key\.pem|id_rsa'` returns empty
- [ ] `git grep -r 'xpub\|zpub\|Ltub'` returns only `.env.example` (not a real xpub value) and documentation
- [ ] `git grep -r 'SERVER_SALT\s*=' ` returns only `.env.example` and documentation, no hardcoded values
- [ ] `git grep -r 'WALLET_ENC_KEY\s*='` same — only `.env.example` and documentation
- [ ] Binary artifacts (`naroom`, `naroom_bin`, `naroom_linux`, etc.) are in `.gitignore` and not tracked

---

## 2. Code Review Gates

- [ ] All plain wallet address storage paths use `HMAC-SHA256(HASH_KEY, ...)` before insert — no new columns store raw addresses in `listings`, `responses`, `chat_rooms`, `sessions`, `reputation`, `invoices`
- [ ] Any new `wallet_sessions` access that needs the raw address decrypts via `crypto.DecryptAddress` and discards immediately — never logs, never writes to another table
- [ ] No new IP logging: check that new middleware or handlers do not call `r.RemoteAddr` without hashing or stripping
- [ ] No new session token storage without SHA-256: any code inserting into `sessions` must store `sha256.Sum256(token)`, not the raw token
- [ ] Role enforcement: any new endpoint accessible by both roles has an explicit role check at handler entry (before DevMode block)
- [ ] New WebSocket endpoints extract auth token from `Sec-WebSocket-Protocol` header, not from URL query string
- [ ] New invariants (if any) are added to `docs/INVARIANTS.md` with enforcement location and test reference

---

## 3. Test Gates

Run the full test suite and confirm all pass:

```bash
bash scripts/selftest.sh
```

- [ ] Go unit tests: all 26 pass (or more if new tests were added)
- [ ] E2E tests: all 25 pass (or more if new tests were added)
- [ ] Frontend type check: `npm run check` exits 0
- [ ] No skipped or pending tests without documented reason

If new invariants were added in this release:

- [ ] Corresponding E2E or unit test added for each new invariant
- [ ] `docs/TEST_MATRIX.md` updated to reflect coverage

---

## 4. Frontend Build Check

```bash
cd frontend
npm run check
npm run build
```

- [ ] `npm run check` (SvelteKit type-check) exits 0 with no errors
- [ ] `npm run build` completes without errors
- [ ] Build output does not contain any hardcoded wallet addresses, API keys, or environment secrets

---

## 5. Database Migration Check

- [ ] Any schema changes are implemented as additive migrations in `internal/db/db.go` (new columns with defaults, new tables, or DROP TABLE for removed tables)
- [ ] No existing column is renamed or dropped without a migration that preserves or transforms existing data
- [ ] `internal/db/schema.sql` reflects the final intended schema after all migrations
- [ ] Startup migration runs idempotently: applying the same migration twice does not fail or corrupt data
- [ ] If `wallet_sessions` schema changed: verify `db.go:MigrateWalletEncryption` still correctly identifies and encrypts any unencrypted rows on startup

---

## 6. Deployment Checklist

Before deploying to production:

- [ ] All required environment variables are set in the production environment:
  - `SERVER_SALT` — 64-char hex, randomly generated, not the same as any dev value
  - `HASH_KEY` — 64-char hex, randomly generated, separate from `SERVER_SALT`; primary HMAC key for wallet hashing (falls back to `SERVER_SALT` if unset, but separate key is strongly recommended)
  - `WALLET_ENC_KEY` — 32-char string, randomly generated, not derived from `SERVER_SALT`
  - `DB_PATH` — points to a persistent volume, not a temp directory
  - `PORT` — set to desired listen port
  - `BTC_XPUB` — valid BIP-32 extended public key (xpub or zpub format)
  - `LTC_XPUB` — valid LTC extended public key (Ltub format)
  - `MEMPOOL_API` — mempool.space API base URL
  - `BLOCKCYPHER_API` — BlockCypher LTC API base URL
  - `DEV_MODE=false`
- [ ] `HASH_KEY`, `SERVER_SALT`, and `WALLET_ENC_KEY` are all distinct values (each serves a different function; reusing them across purposes is a mistake)
- [ ] `DEV_MODE` is explicitly set to `false` (default is false, but confirm it is not accidentally set to true)
- [ ] The previous production database has been backed up before deploying schema migrations
- [ ] If `WALLET_ENC_KEY` changed: run migration path to re-encrypt existing `wallet_address_enc` rows before going live (or ensure old data is migrated)
- [ ] Binary built for target OS/arch: `GOOS=linux GOARCH=amd64 go build -o naroom ./cmd/naroom/main.go`

---

## 7. Post-Deploy Verification

After deploying, confirm the instance is healthy:

- [ ] `GET /healthz` returns `200 OK`
- [ ] Register a test wallet using DEV mode or a known wallet + signature; confirm `session_token` is returned
- [ ] Create a test listing; confirm `listing_id` and `invoice_id` are returned and listing appears on `GET /board/{city}`
- [ ] Confirm server logs do not contain raw IP addresses, wallet addresses, or session tokens
- [ ] Confirm Telegram bot (if configured) is responding to `/start` — send a message and check for reply
- [ ] Monitor error logs for the first 10 minutes after deploy for unexpected panics or database errors
