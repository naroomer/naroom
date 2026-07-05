# NA Room — Fable Security Audit Package

**Audit date:** 2026-07-05  
**Domain:** naroom.net  
**Package:** `naroom_current_final_audit.tar.gz`  
**Snapshot:** `naroom_fable5_current_audit.txt`

---

## What this package contains

| File | Purpose |
|------|---------|
| `naroom_fable5_current_audit.txt` | Full source snapshot: all Go, JS, SQL, and doc files concatenated with paths |
| `naroom_current_final_audit.tar.gz` | Compressed archive of the full source tree (excludes binaries, DB, .env, node_modules) |
| `FABLE_AUDIT_README.md` | This file |

---

## Test results (as of this package)

```
go test ./...        26/26 PASS  (2 packages with tests)
npm run check        0 errors, 48 warnings (pre-existing a11y/CSS)
npm run build        PASS
bash scripts/selftest.sh  34/34 E2E PASS
```

---

## Architecture summary

NA Room is an anonymous P2P peer support platform for people in addiction recovery.

- **Backend:** Go 1.22, chi router, SQLite WAL, modernc.org/sqlite (pure Go)
- **Frontend:** SvelteKit (Node adapter), E2E encryption via tweetnacl (nacl.box)
- **Crypto:** BTC + LTC, mempool.space + BlockCypher + Blockchair APIs
- **Privacy:** no accounts, no emails, wallet address → HMAC-SHA256 hash only

---

## Wallet verification model (corrected in Sprint 5)

**Step 1 — Balance pre-check** (`POST /wallet/register`):  
Checks that the submitted wallet address holds ≥ threshold balance (client: $150, peer: $1000).  
**This is NOT ownership proof.** No cryptographic signature required.  
No `/wallet/challenge` endpoint exists or is planned.

**Step 2 — Payment-time ownership proof** (`invoice_watcher.go:verifySenderAndBalance`):  
When a blockchain payment arrives, the watcher:
1. Computes `HMAC-SHA256(HASH_KEY, sender_address)` for every input address in the transaction
2. Compares against `invoices.payer_address` (stored hash, never plain text)
3. Checks that the matching sender's post-payment balance ≥ threshold ($975 for peers, $135 for clients)
4. **Only if both pass:** creates the chat room inside a DB transaction

**No chat room is ever created based on `/wallet/register` alone.**

---

## Payment rules

| Rule | Behavior |
|------|---------|
| Correct sender + sufficient post-payment balance | Chat room created ✅ |
| Wrong sender | Invoice immediately rejected ❌ |
| Underpayment (< 99% of required) | Invoice stays pending, retried next cycle |
| Correct sender + balance drops below threshold after payment | Chat room NOT created ❌ |
| Multi-transaction (each tx < required) | Not aggregated — only a single tx meeting the full amount is accepted |

---

## Known limitations (documented, not bugs)

| Limitation | Impact | Status |
|-----------|--------|--------|
| CoinJoin / custodial sends | Exchange withdrawal may include multiple unrelated input addresses; any matching hash passes | Documented, low risk in practice |
| Single-tx payment requirement | Users must send the full invoice amount in one transaction | Documented in UI |
| RP-4 abuse ban not enforced in endpoints | `banned_until` is set correctly but not checked in `/board`, `/listing`, `/respond` | Known gap, documented in INVARIANTS.md |
| `/wallet/challenge` absent | No cryptographic ownership proof at registration — payment sender is the proof | Intentional design decision |

---

## Files excluded from archive

- `*.db`, `*.wal`, `*.shm` — database files
- `.env` — secrets
- `node_modules/`, `build/`, `.svelte-kit/` — build artifacts
- `.git/` — version control
- `docs/archive/` — superseded internal documents
- `naroom-linux`, `naroom-backup` — compiled binaries

---

## Stale reference scan (clean)

| Pattern | Result |
|---------|--------|
| `naroom.io` | Only in BACKLOG.md as "no longer used" note — correct |
| `github.com/naroom` | Not present |
| `/api/upload` | Not present |
| `25/25` old test count | Not present in active docs |
| `wallet ownership proof at registration` | Corrected in all active docs |
| `027` open/failing reference | Corrected — test now passes, describes trust model |
| `wallet_challenge_test.go` exists | TEST_MATRIX.md updated with REMOVED status |
