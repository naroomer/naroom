# Threat Model — NA Room

## Core Principle

> Providers may know who operates the server, but the server and providers should not be able to identify users, read chats, or reconstruct sensitive activity beyond minimum operational metadata.

## What Each Party Knows

### Server Operator (live, running server)

| Data | Can See? | Notes |
|------|----------|-------|
| Chat message content | No | E2E encrypted, server stores only nonce + ciphertext |
| Messages after close | No | Deleted on session close |
| Real wallet addresses in listings/chats | No | Stored as HMAC-SHA256 hashes |
| Real wallet addresses in active sessions | Partial | `wallet_sessions.wallet_address_enc` is AES-256-GCM encrypted; decrypted only transiently for blockchain API calls |
| Listing metadata (city, dep. type, urgency) | Yes | Required for board to function |
| Session timing and duration | Yes | Operational metadata |
| Which wallets are currently active | Yes | Via `wallet_sessions` |
| User IP addresses | No | Stripped by middleware, not logged |

### Attacker Who Steals the Database (without HASH_KEY)

| Data | Can Recover? | Notes |
|------|-------------|-------|
| Message content | No | E2E encrypted, already deleted |
| Wallet addresses in listings/chats | No | HMAC hashes without key are unlinkable |
| Wallet addresses of active sessions | No (without WALLET_ENC_KEY) | `wallet_sessions.wallet_address_enc` is AES-256-GCM ciphertext; requires `WALLET_ENC_KEY` to decrypt |
| Listing metadata | Yes | City, type, timestamps stored plain |
| Session social graph | Partial | Can see which listing_id connected to which room, but not who |

### Attacker Who Steals Database + HASH_KEY

| Data | Can Recover? | Notes |
|------|-------------|-------|
| Message content | No | E2E encrypted, already deleted |
| Wallet addresses in listings/chats | Yes | Can reverse HMAC hashes |
| Link wallet to listing/chat activity | Yes | This is the primary residual risk |
| Link two listings to the same wallet | Yes | Both hashed with same key |

### Third-Party Blockchain API Providers (mempool.space, BlockCypher)

| Data | Can See? | Notes |
|------|----------|-------|
| Wallet addresses being checked | Yes | Balance checker queries them directly |
| Invoice addresses being watched | Yes | Invoice watcher polls them |
| Link "NA Room server queries this wallet" | Yes | IP of the server making the request |

Mitigation: route API calls through Tor in production, or run own Bitcoin/Litecoin nodes.

### Anyone Watching the Blockchain

| Data | Can See? | Notes |
|------|----------|-------|
| Payments to NA Room invoice addresses | Yes | All blockchain transactions are public |
| Which wallet paid which invoice | Yes | On-chain payment graph is public |
| Link a wallet to NA Room activity | Yes | If they know the NA Room invoice address range |

Mitigation: unique single-use invoice addresses (already implemented), user education.

## Key Controls Summary

| Control | Status |
|---------|--------|
| E2E message encryption (X25519 + XSalsa20-Poly1305) | Implemented |
| Messages deleted on close | Implemented |
| Wallet addresses hashed (HMAC-SHA256) in listings/chats | Implemented |
| No IP logging | Implemented |
| No accounts / no PII | By design |
| Session tokens stored as SHA-256 hash only | Implemented |
| `wallet_address` removed from `sessions` table | Implemented |
| Single-use invoice addresses | Implemented |
| `wallet_sessions.wallet_address_enc` AES-256-GCM at rest | Implemented (Sprint 1) |
| Own BTC/LTC nodes (no third-party blockchain API) | Not yet |
| Tor hidden service | Not yet (production) |

## Residual Risks (Known, Accepted)

1. **`wallet_sessions.wallet_address_enc` requires `WALLET_ENC_KEY`.** Addresses are encrypted at rest and decrypted only transiently for blockchain API calls. An attacker with both the database dump and `WALLET_ENC_KEY` can recover active session addresses. Mitigation: store `WALLET_ENC_KEY` separately from the database (different host, secret manager, etc.).

2. **Listing metadata is plain text.** City + dependency type + timestamp can narrow identity in small communities. Mitigation: coarser timestamps, no sub-city granularity.

3. **Blockchain payment graph is public.** The peer's wallet is visible on-chain. Mitigation: user education, never claim "anonymous payments."

4. **Frontend delivery trust.** A malicious operator could ship altered JS. Mitigation: open source code + reproducible builds allow users to verify. Document the limitation honestly.
