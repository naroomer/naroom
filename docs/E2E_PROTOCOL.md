# NA Room — E2E Test Protocol

How to run, add, and debug E2E tests.

---

## Prerequisites

```bash
# Node.js 18+
node --version

# Backend built
cd /Users/dmitrijulybin/Projects/shelter/naroom
go build ./...
```

---

## Running Tests

### Full suite

```bash
cd /Users/dmitrijulybin/Projects/shelter/naroom/e2e
for f in tests/0*.js; do node "$f"; done
```

### Single test

```bash
cd /Users/dmitrijulybin/Projects/shelter/naroom/e2e
node tests/001_happy_path.js
```

### With output file

```bash
cd /Users/dmitrijulybin/Projects/shelter/naroom/e2e
for f in tests/0*.js; do node "$f"; done 2>&1 | tee /tmp/e2e-run.log
```

---

## How Tests Work

Each test file:
1. Imports `TestServer` from `lib/server.js` — spawns the backend binary on a random port with a temp SQLite DB
2. Sets `DEV_MODE=true` — payments auto-confirm, rate limiting disabled
3. Performs HTTP calls via `ApiClient` (`lib/http.js`)
4. Uses `Runner` (`lib/runner.js`) to register pass/fail steps
5. Calls `srv.stop()` in `finally` — kills the process, deletes temp DB

### TestServer options

```js
const srv = new TestServer();              // devMode: true (default)
const srv = new TestServer({ devMode: false }); // rate limiting active
```

### DevMode vs Production behavior

| Behavior | DevMode | Production |
|----------|---------|-----------|
| Payment verification | skip (auto-confirm) | blockchain API required |
| Rate limiting | disabled | enabled |
| `WALLET_ENC_KEY` | derived from SERVER_SALT | must be set explicitly |
| Session TTL | 24h (same) | 24h |
| Listing TTL | configured via env | configured via env |

---

## Adding a New Test

1. Create `tests/NNN_descriptive_name.js`
2. Export an `async function run()` that returns nothing (throws on failure)
3. Use the `Runner` to track sub-steps:

```js
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { assertStatus } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

export async function run() {
  console.log('\n=== NNN: Test Name ===');
  const srv = new TestServer();
  const t = new Runner('NNN_test_name');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    await t.run('step description', async () => {
      const r = await api.get('/some/endpoint');
      assertStatus(r, 200);
    });

  } finally {
    await srv.stop();
    t.report();
  }
}

run().catch(e => { console.error(e); process.exit(1); });
```

4. Add coverage to `docs/TEST_MATRIX.md`

---

## Available Assertions (`lib/assert.js`)

| Function | Description |
|----------|-------------|
| `assertStatus(resp, code)` | Throws if HTTP status ≠ expected |
| `assertHasField(body, field)` | Throws if field missing from JSON body |
| `assertNoField(body, field)` | Throws if field unexpectedly present |
| `assertNoRoom(resp)` | Asserts no chatroom in response |
| `assertDbCount(srv, sql, n)` | Run raw SQL on test DB, assert row count |
| `pollUntil(fn, opts)` | Retry fn until truthy; opts: `{ maxMs, intervalMs }` |
| `pass(label)` | Print green pass line |
| `sleep(ms)` | Promise-based sleep |

---

## Available Helpers (`lib/http.js` — `ApiClient`)

```js
api.verifyWallet(address, currency, role)  // POST /wallet/register
api.createListing(address)                 // POST /listing/create
api.respond(listingId, peerToken, keys)    // POST /listing/{id}/respond
api.accept(responseId, clientToken)        // POST /response/{id}/accept
api.getChatRoom(roomId, token)             // GET /chat/{room_id}
api.closeChat(roomId, token)               // POST /chat/{room_id}/close
api.get(path, token?)                      // GET with optional Bearer
api.post(path, body, token?)               // POST with optional Bearer
```

---

## TestServer Direct DB Access

For asserting internal state that has no API endpoint:

```js
// Insert/update
srv.db(`UPDATE listings SET status='closed' WHERE id=?`, [id]);

// Query (returns array of row objects)
const rows = srv.dbQuery(`SELECT * FROM wallet_sessions`);
```

---

## Test Naming Convention

```
NNN_feature_or_scenario.js
```

- **001–049** — happy paths and core flows
- **050–099** — edge cases and failure modes
- **100+** — reserved for concurrency / stress tests

Current tests span 001–014, all in the happy-path range. Edge case tests should start at 050.

---

## Debugging

### Verbose backend logs

```bash
# Set LOG_LEVEL=debug (if supported) or watch stderr
node tests/001_happy_path.js 2>&1 | cat
```

### Keep temp DB after failure

In `lib/server.js`, comment out `fs.unlinkSync(this.dbPath)` in `stop()` temporarily, then:

```bash
sqlite3 /tmp/naroom-test-XXXXX.db '.tables'
sqlite3 /tmp/naroom-test-XXXXX.db 'SELECT * FROM invoices'
```

### Single step isolation

Use `t.run()` labels in conjunction with `pass()` calls to bisect which step fails.

---

## CI Integration

To run in CI (GitHub Actions or similar):

```yaml
- name: Build backend
  run: cd naroom && go build ./...

- name: Unit tests
  run: cd naroom && go test -count=1 -timeout 120s ./...

- name: Frontend check
  run: cd naroom/frontend && npm ci && npm run check

- name: Frontend build
  run: cd naroom/frontend && npm run build

- name: Run E2E tests
  run: |
    cd naroom/e2e
    for f in tests/0*.js; do node "$f" || exit 1; done
```

No external services required — all tests use DevMode with local SQLite.

### One-command selftest

```bash
cd naroom && ./scripts/selftest.sh
```

Runs all four stages: build → unit → frontend check+build → E2E. Fails fast on build failure; fails after all stages otherwise. Exit code 1 on any failure.

---

## Test Invocation Pattern

Every test file **must** include a top-level invocation after the `run()` export:

```js
run().then(ok => { if (!ok) process.exit(1); }).catch(e => { console.error(e); process.exit(1); });
```

Without this, `node "$f"` loads the module but never calls `run()`, exiting 0 trivially.
All tests 001-019 include this invocation.

---

## Known Gaps (as of 2026-06-25, after Sprint 1)

Resolved in Sprint 1: RS-4 (015), SE-3 (016), RS-1 (017), IN-5/RS-5 (018), LS-3 (019).
Also resolved: ID-3 (wallet_challenges table dropped entirely).

Remaining gaps:

| Priority | Test | Invariant |
|----------|------|-----------|
| HIGH | Wrong-wallet payment rejected (E2E) | IN-4 |
| HIGH | Cancel cooldown enforcement | RS-3 |
| MEDIUM | Balance checker fail → close chat | WK-3 |
| MEDIUM | DB assertion: messages deleted after close | CH-4 |
| MEDIUM | DB assertion: payer_address is HMAC hash | IN-1 |
| LOW | SE-4: DevMode header rejected in prod | SE-4 |
| LOW | RP-4: Abuse ban thresholds | RP-4 |
| LOW | WK-1: Message TTL cleanup | WK-1 |
| LOW | wallet_sessions TTL cleanup | WK-3 |
| LOW | No IP in logs | ID-5 |

See `docs/INVARIANTS.md` for the full invariant definitions.
