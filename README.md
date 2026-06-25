# NA Room

NA Room is an anonymous peer support platform for addiction recovery. Clients post a help request; peers (verified volunteers with a minimum BTC/LTC balance as a trust signal) browse and respond. Once matched, both parties communicate in an end-to-end encrypted chat room. No accounts, no email, no usernames.

## Privacy guarantees

- Chat messages are end-to-end encrypted (X25519 + XSalsa20-Poly1305 via TweetNaCl). The server stores only nonce + ciphertext and cannot decrypt anything.
- Messages are permanently deleted when the session closes (also TTL-cleaned at 24 hours).
- Wallet identity in listings, responses, and chat rooms is stored only as an HMAC-SHA256 hash. Without the server's `SERVER_SALT` key the database cannot be linked to real wallet addresses.
- IP addresses are not logged. Rate limiting uses a hashed /24 subnet, never persisted.
- No accounts, no email, no phone numbers, no usernames.
- Session tokens: only the SHA-256 hash is stored in the database.
- Active wallet addresses are stored AES-256-GCM encrypted at rest (`WALLET_ENC_KEY`).

See [SECURITY.md](SECURITY.md) and [THREAT_MODEL.md](THREAT_MODEL.md) for residual risks stated honestly.

## Quick start

```bash
git clone https://github.com/naroomer/naroom.git
cd naroom
cp .env.example .env
# Edit .env — set SERVER_SALT, WALLET_ENC_KEY, BTC_XPUB, LTC_XPUB at minimum
go run ./cmd/naroom/main.go
```

For development without blockchain payments:

```bash
DEV_MODE=true go run ./cmd/naroom/main.go
```

Frontend (SvelteKit, in `/frontend`):

```bash
cd frontend
npm install
npm run dev
```

## Running tests

```bash
bash scripts/selftest.sh
```

This runs: Go unit tests, frontend type-check, and 25 E2E tests against a real Go backend on a temp SQLite database.

Current status: **25/25 E2E · 26/26 unit — all green**

## Documentation

- [SECURITY.md](SECURITY.md) — what is and is not protected, honest claim
- [THREAT_MODEL.md](THREAT_MODEL.md) — per-party threat analysis
- [PRIVACY_MODEL.md](PRIVACY_MODEL.md) — data collection, encryption details, residual risks
- [ARCHITECTURE.md](ARCHITECTURE.md) — system design, request flows, component descriptions
- [TESTING.md](TESTING.md) — test suite guide, E2E test list
- [docs/INVARIANTS.md](docs/INVARIANTS.md) — security invariants with code references
- [docs/TEST_MATRIX.md](docs/TEST_MATRIX.md) — invariant-to-test coverage map
- [SELF_HOSTING.md](SELF_HOSTING.md) — self-hosting guide

## License

MIT
