# Security Policy — NA Room

## Wallet Verification Model (Two-Step)

NA Room verifies wallet control in two distinct steps:

| Step | When | What is verified |
|------|------|-----------------|
| Balance pre-check | `POST /wallet/register` | Wallet address holds ≥ threshold balance at time of registration. **NOT ownership proof** — no cryptographic signature required. |
| Payment proof | On-chain (invoice watcher) | Payment sender address hashes to the registered wallet hash (IN-3), AND sender's post-payment balance still meets threshold (IN-5). |

**Chat room creation is gated on both:** the invoice watcher's `verifySenderAndBalance` must return true before `confirmInvoice` creates the chat room. A successful `/wallet/register` alone — without a confirmed payment — never opens a chat room.

**No challenge-signature is required or planned.** There is no `/wallet/challenge` endpoint. Bitcoin/Litecoin message signing (challenge-response) is intentionally absent from the architecture. Wallet control is proven at payment time by sender address verification — this is the ownership proof. This is a deliberate design decision and is not a known gap.

**Single-transaction payment requirement.** Each invoice must be settled by a single transaction. Multiple transactions to the same invoice address are not aggregated — only the first transaction that meets the full amount threshold is evaluated.

**Wrong sender → invoice rejected immediately.** When the invoice watcher detects a payment, the sender's address is hashed and compared to `invoices.payer_address`. If no input address matches, the invoice is marked `rejected` before any balance check occurs. The listing or chat room is never activated.

---

## What NA Room Protects

| What | How |
|------|-----|
| Chat message content | End-to-end encrypted (X25519 + XSalsa20-Poly1305 via TweetNaCl). The server stores only `nonce + ciphertext` and cannot decrypt anything. |
| Messages after session | Deleted when **both** sides close (second `POST /chat/{room_id}/close`). If only one side has closed (status `peer_left` or `client_left`), messages remain for the other side to read. A 24h TTL worker deletes all messages unconditionally. |
| Wallet identity in listings, chats, responses, invoices | Never stored as plain text in these tables. Stored as `HMAC-SHA256(HASH_KEY, address)`. Without the server's `HASH_KEY` the database cannot be linked to real wallets. |
| IP addresses | Not logged. Rate limiting uses a hashed /24 subnet, never persisted. |
| Who you are | No accounts, no email, no phone, no username. |

## What NA Room Does NOT Fully Protect

| What | Why |
|------|-----|
| Wallet addresses of active users | `wallet_sessions.wallet_address_enc` stores AES-256-GCM encrypted addresses (key: `WALLET_ENC_KEY`). Decrypted transiently inside balance checker and invoice watcher workers only. Attacker with only the database cannot read addresses without `WALLET_ENC_KEY`. Attacker with both database and `WALLET_ENC_KEY` can decrypt active session addresses. |
| Listing metadata | City, dependency type, urgency, language, and timestamp are stored in plain text. The board must be searchable. In small communities or rare combinations this metadata can be identifying. |
| Blockchain payment graph | Invoice addresses are unique and never reused, but payments are on-chain and publicly visible. Anyone watching the blockchain can link an address to platform activity. |
| Live server operator access | The server operator can observe session timing, listing metadata, and current wallet addresses in `wallet_sessions`. They cannot read chat content or reconstruct wallet history from HMAC hashes without the `HASH_KEY`. |
| Frontend code delivery | E2E encryption protects against passive storage compromise. It does not protect against a malicious operator who ships altered frontend JavaScript or substitutes public keys. |
| Payment-as-ownership-proof | We verify that the sender of the blockchain transaction matches the registered wallet. BTC/LTC transactions can have multiple inputs from different addresses; we check all of them. However, this is "transaction participation proof," not cryptographic wallet ownership proof. Edge cases: CoinJoin, custodial sends, and exchange withdrawals may involve multiple unrelated addresses in a single transaction. |

## Threat Model Summary

**Against passive database leak (no `HASH_KEY`, no `WALLET_ENC_KEY`):**
Strong for listings, chats, and responses — wallet hashes are unlinkable.
Wallet addresses in `wallet_sessions` are AES-256-GCM encrypted — unreadable without `WALLET_ENC_KEY`.

**Against database leak with `HASH_KEY`:**
Medium. HMAC hashes in listings/chats/responses can be reversed to wallet addresses. Message content is still E2E encrypted and already deleted.

**Against live server compromise or malicious operator:**
Limited. The operator can observe listing metadata, session timing, and active wallet addresses in `wallet_sessions`. They cannot read encrypted messages.

**Against blockchain correlation:**
Medium-to-weak. Payment addresses are unique and single-use, but on-chain transactions are public.

## Wallet Address Encryption — Current State (Sprint 1 + Sprint 2)

`wallet_sessions.wallet_address` was the main residual risk. **Implemented mitigations:**

1. **AES-256-GCM encryption at rest** ✅ — `wallet_sessions.wallet_address_enc` stores the address encrypted with `WALLET_ENC_KEY` (AES-256-GCM, random nonce). Plain address is only decrypted inside balance/payment workers and immediately discarded.
2. **`wallet_challenges` table dropped** ✅ — The table previously stored plain `wallet_address` and was entirely orphaned (no handler used it). It has been dropped in Sprint 1 (`DROP TABLE IF EXISTS wallet_challenges` migration runs on startup).
3. **`reconnection_hashes` column removed** ✅ — Stub feature that was never read. Removed from schema and handler code.
4. **Aggressive TTL cleanup** ✅ — Session cleanup workers run every 6 hours. Shorter TTLs reduce the exposure window.
5. **Backup hygiene** — SQLite backups (Backblaze B2) should be encrypted at the bucket level; `wallet_address_enc` is already ciphertext.
6. **No plaintext logs** — wallet addresses must never appear in log output.

## Honest Claim

> NA Room hides chat content from the server and minimizes durable identity metadata. Its strongest guarantees are: encrypted ephemeral messages, HMAC-hashed wallet identifiers in listings and chats, AES-256-GCM encrypted wallet addresses at rest, and no accounts or personal data of any kind.
>
> Its primary limitation: active wallet addresses in `wallet_sessions` are AES-256-GCM encrypted with `WALLET_ENC_KEY`. They are decrypted transiently only inside the balance checker and invoice watcher workers. An attacker who obtains both the database and `WALLET_ENC_KEY` can recover active session addresses. Keep `WALLET_ENC_KEY` separate from the database (environment variable, secret manager, separate host).

## Reporting a Vulnerability

If you discover a security vulnerability, please open a GitHub issue marked `[security]` or email the maintainer directly. We will acknowledge within 48 hours and aim to patch critical issues within 7 days.

Please do not disclose publicly until a fix is available.
