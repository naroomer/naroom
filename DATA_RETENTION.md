# Data Retention Policy — NA Room

## What We Collect and For How Long

| Data | Stored | Format | Deleted |
|------|--------|--------|---------|
| IP address | Never | — | N/A |
| Browser fingerprint | Never | — | N/A |
| Analytics / tracking | Never | — | N/A |
| Real name, email, phone | Never | — | N/A |
| Wallet address (listings, chats, responses) | Yes | HMAC-SHA256 hash only | When listing/chat expires |
| Wallet address (active session) | Yes | Plain text | When session expires (24h TTL) |
| Chat messages | Yes, while session is active | E2E encrypted (server cannot read) | Permanently on session close |
| Session token | Yes | SHA-256 hash only (raw token never stored) | After 24h or on revoke |
| Listing metadata (city, type, urgency, language) | Yes | Plain text | When listing expires |
| Listing timestamp | Yes | Unix timestamp | When listing expires |
| Session duration | Yes | Start/end timestamps | When chat room is cleaned up |
| Reputation score | Yes | Aggregate only (thumbs up/down count, sessions count) | Never (this is the peer's track record) |
| Payment invoice (address, amount) | Yes | Plain text | Not deleted (financial record) |
| Abuse reports | Yes | Hashed client identity only | After 30 days |

## What "Anonymous" Means Here

The word "anonymous" on this platform means:

- **No identity data is collected.** We never ask for or store names, email addresses, phone numbers, or any PII.
- **Wallet addresses are hashed.** In listings and chats, your wallet address is stored only as a keyed hash. Without the server's secret key, the hash cannot be linked to a real address.
- **Messages are end-to-end encrypted.** The server cannot read your chat. Messages are deleted when the session closes.
- **IP addresses are not logged.**

It does **not** mean:

- **Blockchain payments are not anonymous.** When you pay an invoice in Bitcoin or Litecoin, that payment is visible on the public blockchain. Anyone watching the blockchain can see which wallet sent funds to a NA Room invoice address.
- **Live server observation is not possible.** The operator can see that a chat session occurred, how long it lasted, and which city/type combination was in the listing. They cannot see who you are or what was said.

## Deletion Guarantees

- Chat messages: deleted immediately when a session is closed by the client. Not recoverable.
- Expired listings: cleaned up by TTL worker (default 24h visible window).
- Expired sessions: cleaned up by TTL worker after 24h.
- Expired challenges: cleaned up after 5 min.
- Peer-left rooms (not explicitly closed): expired after 24h.

All deletion is permanent. NA Room does not maintain backups of chat content.

## What the Server Salt Protects

The server holds one secret (`HASH_KEY`) that is the key to all wallet address hashes in the database.

- If the database is stolen **without** the `HASH_KEY`: wallet addresses in listings and chats are unrecoverable.
- If the database is stolen **with** the `HASH_KEY`: wallet addresses can be recovered for listings/chats. Messages are still unreadable (E2E encrypted) and already deleted.

The `HASH_KEY` is never stored in the database or source code. It is an environment variable injected at startup.
