// 047_telegram_reconnect.js — Telegram reconnect Playwright + API test
//
// NOT included in selftest.sh (same pattern as 043_browser_renewal.js).
// Run standalone:
//   node e2e/tests/047_telegram_reconnect.js
//
// Prerequisites:
//   cd e2e && npm i          (playwright already in devDependencies)
//   npx playwright install chromium
//
// ── API tests (no browser) ──────────────────────────────────────────────────
//   AT-1:  Two concurrent token requests leave exactly one live token.
//   AT-2:  Issuance rollback: SQLite BEFORE INSERT trigger aborts replacement;
//          HTTP 500; previous token rolled back to used=0; still consumable.
//   AT-3:  Null-principal token (injected) rejected by webhook; used rolled back.
//   AT-4:  Active listing with expired visible_until rejects token issuance (409).
//   AT-5:  Active listing with expired visible_until rejects webhook; token rolled back.
//   AT-6:  Old (invalidated) token rejected by webhook (400).
//   AT-7:  Fresh token consumed by webhook (200).
//   AT-8:  Replay of consumed token rejected (400).
//   AT-9:  Second webhook replaces binding; old chat_id deactivated; exactly one active.
//
// ── Browser tests (Playwright / Chromium) — isolated listing2 fixture ───────
//   BT-0:  Issue initial token; expire it; webhook returns 400.
//   BT-1:  After navigate-away → reload → Connect Telegram control required (not connected).
//   BT-2:  Click Connect Telegram → fresh t.me deep link required.
//   BT-3:  Consume via webhook; page poll renders Connected.
//   BT-4:  Non-owner session sees no Telegram section.
//   BT-5:  Unauthenticated GET confirm → 401; non-owner → 403.
//   BT-6:  No extra listing/invoice/response/chat; exactly one active binding.
//
// ── Infrastructure ──────────────────────────────────────────────────────────
//   - Binary built with go build -tags dev (not go run).
//   - Dynamic ports reserved atomically; verified closed after cleanup.
//   - Process-group SIGTERM, SIGKILL fallback; process exit awaited.
//   - Binary path checked for strays after teardown.

import { chromium }            from 'playwright';
import { spawn, execFileSync } from 'child_process';
import { mkdtempSync, rmSync } from 'fs';
import { tmpdir }              from 'os';
import { join }                from 'path';
import net                     from 'net';

// ── Constants ─────────────────────────────────────────────────────────────────
const BACKEND_DIR  = new URL('../../', import.meta.url).pathname.replace(/\/$/, '');
const FRONTEND_DIR = join(BACKEND_DIR, 'frontend');
const VITE_BIN     = join(FRONTEND_DIR, 'node_modules/.bin/vite');

const TEST_SALT      = 'e2e-test-salt';
const TEST_ENC_KEY   = 'e2e-test-wallet-enc-key-32bytes!';
const WEBHOOK_SECRET = 'test-webhook-secret-047';
const WALLET1        = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const WALLET2        = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';
const CHAT_A         = '9100000001'; // AT tests first chat
const CHAT_B         = '9100000002'; // AT tests second chat (replacement)
const CHAT_C         = '9100000003'; // AT-2 rollback verification consumer
const CHAT_D         = '9100000004'; // BT browser consumer

// ── Port helpers ─────────────────────────────────────────────────────────────
const sleep = ms => new Promise(r => setTimeout(r, ms));

// Reserve N sockets simultaneously — guaranteed distinct ports.
async function getFreePorts(count) {
  const servers = await Promise.all(
    Array.from({ length: count }, () =>
      new Promise((resolve, reject) => {
        const srv = net.createServer();
        srv.listen(0, '127.0.0.1', () => resolve(srv));
        srv.on('error', reject);
      }),
    ),
  );
  const ports = servers.map(s => s.address().port);
  await Promise.all(servers.map(s => new Promise(r => s.close(r))));
  return ports;
}

async function waitForPort(port, timeout = 40000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    const ok = await new Promise(resolve => {
      const c = net.createConnection(port, '127.0.0.1');
      c.on('connect', () => { c.destroy(); resolve(true); });
      c.on('error',   () => resolve(false));
    });
    if (ok) return;
    await sleep(300);
  }
  throw new Error(`Port ${port} not ready within ${timeout}ms`);
}

async function waitPortClosed(port, timeout = 8000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    const open = await new Promise(resolve => {
      const c = net.createConnection(port, '127.0.0.1');
      c.on('connect', () => { c.destroy(); resolve(true); });
      c.on('error',   () => resolve(false));
    });
    if (!open) return true;
    await sleep(200);
  }
  return false;
}

// Send SIGTERM to process group; SIGKILL fallback after timeoutMs; await exit.
async function killGroup(proc, label, timeoutMs = 5000) {
  if (!proc || proc.exitCode !== null || proc.pid == null) return;
  const pid = proc.pid;
  console.log(`  [cleanup] SIGTERM pgid=${pid} (${label})`);
  return new Promise(resolve => {
    const kill = setTimeout(() => {
      console.log(`  [cleanup] SIGKILL pgid=${pid} (${label}) — did not exit`);
      try { process.kill(-pid, 'SIGKILL'); } catch {}
      resolve();
    }, timeoutMs);
    proc.on('exit', () => { clearTimeout(kill); resolve(); });
    try { process.kill(-pid, 'SIGTERM'); } catch { clearTimeout(kill); resolve(); }
  });
}

// ── DB helper ─────────────────────────────────────────────────────────────────
function db(dbPath, sql) {
  return execFileSync('sqlite3', [dbPath, sql], { encoding: 'utf8' }).trim();
}

// ── Assertion helpers ─────────────────────────────────────────────────────────
let passed = 0;
let failed = 0;

function pass(label) {
  console.log(`  ✓ ${label}`);
  passed++;
}

function fail(label) {
  console.error(`  ✗ ${label}`);
  failed++;
  throw new Error(label);
}

function assert(cond, label) {
  if (!cond) fail(label);
  else pass(label);
}

// ── HTTP helpers ─────────────────────────────────────────────────────────────
async function apiCall(base, method, path, body, token) {
  const headers = { 'Content-Type': 'application/json' };
  if (token) headers['Authorization'] = `Bearer ${token}`;
  const opts = { method, headers };
  if (body !== undefined) opts.body = JSON.stringify(body);
  const r = await fetch(`${base}${path}`, opts);
  const b = await r.json().catch(() => ({}));
  return { status: r.status, body: b };
}

async function webhook(base, token, chatID) {
  const r = await fetch(`${base}/telegram/client/webhook`, {
    method:  'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-Telegram-Bot-Api-Secret-Token': WEBHOOK_SECRET,
    },
    body: JSON.stringify({
      message: { text: `/start ${token}`, chat: { id: parseInt(chatID) } },
    }),
  });
  const b = await r.json().catch(() => ({}));
  return { status: r.status, body: b };
}

// Create session + wallet + listing; return { token, listingId }.
async function createFixture(base, wallet) {
  const sr = await apiCall(base, 'POST', '/session/init', { role: 'client' });
  if (sr.status !== 201) throw new Error(`session/init: ${sr.status}`);
  const token = sr.body.session_token;
  const wr = await apiCall(base, 'POST', '/wallet/register',
    { wallet_address: wallet, currency: 'BTC', role: 'client' }, token);
  if (wr.status !== 200 || !wr.body.wallet_linked)
    throw new Error(`wallet/register: ${wr.status}`);
  const cr = await apiCall(base, 'POST', '/listing/create', {
    city: 'tbilisi', dependency_type: 'alcohol', help_type: 'crisis',
    urgency: 'urgent', languages: ['en'], currency: 'BTC',
  }, token);
  if (cr.status !== 201) throw new Error(`listing/create: ${cr.status}`);
  return { token, listingId: cr.body.listing_id };
}

// Poll until listing.status === 'active' (devmode invoice watcher).
async function awaitActive(base, listingId) {
  const deadline = Date.now() + 30000;
  while (Date.now() < deadline) {
    const r  = await fetch(`${base}/listing/${listingId}`);
    const b  = await r.json().catch(() => ({}));
    if (b.status === 'active') return;
    await sleep(1000);
  }
  throw new Error(`Listing ${listingId} did not activate within 30s`);
}

// ── Main ─────────────────────────────────────────────────────────────────────
async function run() {
  console.log('\n=== 047: Telegram Reconnect — browser + API ===\n');

  // Reserve two distinct ports atomically.
  const [BACKEND_PORT, FRONTEND_PORT] = await getFreePorts(2);
  const BACKEND_URL  = `http://127.0.0.1:${BACKEND_PORT}`;
  const FRONTEND_URL = `http://127.0.0.1:${FRONTEND_PORT}`;
  console.log(`backend  port: ${BACKEND_PORT}`);
  console.log(`frontend port: ${FRONTEND_PORT}\n`);

  // Scratch directory — binary, DB, and temp files.
  const tmpDir     = mkdtempSync(join(tmpdir(), 'naroom-047-'));
  const dbPath     = join(tmpDir, 'naroom.db');
  const binaryPath = join(tmpDir, 'naroom-e2e');

  // ── Build backend binary ────────────────────────────────────────────────
  // Use go build (not go run) so we control the exact binary path for teardown verification.
  console.log('Building backend binary…');
  execFileSync(
    'go', ['build', '-tags', 'dev', '-o', binaryPath, './cmd/naroom/main.go'],
    { cwd: BACKEND_DIR, env: { ...process.env } },
  );
  console.log('Binary built.\n');

  // ── Spawn backend ────────────────────────────────────────────────────────
  const backend = spawn(binaryPath, [], {
    env: {
      ...process.env,
      DEV_MODE:               'true',
      SERVER_SALT:            TEST_SALT,
      HASH_KEY:               TEST_SALT,
      WALLET_ENC_KEY:         TEST_ENC_KEY,
      PORT:                   String(BACKEND_PORT),
      DB_PATH:                dbPath,
      TTL_CLEAN_INTERVAL:     '5',
      INVOICE_WATCH_INTERVAL: '2',
      TELEGRAM_WEBHOOK_SECRET:   WEBHOOK_SECRET,
      TELEGRAM_CLIENT_BOT_NAME:  'TestBot047',
      TELEGRAM_CLIENT_BOT_TOKEN: 'test-client-token-047',
      TELEGRAM_HELPER_BOT_TOKEN: 'test-helper-token-047',
    },
    stdio:    ['ignore', 'pipe', 'pipe'],
    detached: true,
  });
  backend.stdout.on('data', () => {});
  backend.stderr.on('data', () => {});

  // ── Spawn Vite dev server (direct binary — single PID, controllable group) ─
  const vite = spawn(
    VITE_BIN,
    ['dev', '--port', String(FRONTEND_PORT), '--host', '127.0.0.1', '--strictPort'],
    {
      cwd: FRONTEND_DIR,
      env: { ...process.env, BACKEND_URL, NO_COLOR: '1', VITE_BACKEND_URL: BACKEND_URL },
      stdio:    ['ignore', 'pipe', 'pipe'],
      detached: true,
    },
  );
  vite.stdout.on('data', d => {
    const s = d.toString();
    if (s.includes('error') || s.includes('Error') || s.includes('500'))
      process.stderr.write('[vite] ' + s);
  });
  vite.stderr.on('data', d => {
    const s = d.toString();
    if (s.includes('error') || s.includes('Error'))
      process.stderr.write('[vite] ' + s);
  });

  // ── Cleanup ──────────────────────────────────────────────────────────────
  let cleanupDone = false;
  async function cleanup() {
    if (cleanupDone) return { backendClosed: true, frontendClosed: true, backendExited: true, viteExited: true, strays: '' };
    cleanupDone = true;

    await Promise.all([killGroup(backend, 'backend'), killGroup(vite, 'vite')]);
    await sleep(400);

    try { rmSync(tmpDir, { recursive: true, force: true }); } catch {}

    const backendClosed  = await waitPortClosed(BACKEND_PORT);
    const frontendClosed = await waitPortClosed(FRONTEND_PORT);
    const backendExited  = backend.exitCode !== null;
    const viteExited     = vite.exitCode !== null;

    // Verify no stray processes referencing our specific binary path.
    let strays = '';
    try {
      strays = execFileSync('pgrep', ['-f', binaryPath], { encoding: 'utf8' }).trim();
    } catch { /* pgrep exits 1 when nothing found — that is the expected result */ }

    console.log(`\n  [cleanup] backend  port ${BACKEND_PORT}: closed=${backendClosed} exited=${backendExited}`);
    console.log(`  [cleanup] frontend port ${FRONTEND_PORT}: closed=${frontendClosed} exited=${viteExited}`);
    if (strays) console.error(`  [cleanup] WARNING: stray backend binary processes: ${strays}`);
    else        console.log('  [cleanup] No stray backend binary processes');

    return { backendClosed, frontendClosed, backendExited, viteExited, strays };
  }

  // Last-resort SIGKILL on unexpected Node exit.
  process.on('exit', () => {
    if (backend.exitCode === null && backend.pid) {
      try { process.kill(-backend.pid, 'SIGKILL'); } catch {}
    }
    if (vite.exitCode === null && vite.pid) {
      try { process.kill(-vite.pid, 'SIGKILL'); } catch {}
    }
  });

  let signalDone = false;
  async function handleSignal(sig) {
    if (signalDone) return;
    signalDone = true;
    console.log(`\nCaught ${sig} — cleaning up…`);
    await cleanup();
    process.exit(130);
  }
  process.on('SIGINT',  () => handleSignal('SIGINT'));
  process.on('SIGTERM', () => handleSignal('SIGTERM'));

  let browser;
  let cleanupResult;

  try {
    await Promise.all([
      waitForPort(BACKEND_PORT, 25000),
      waitForPort(FRONTEND_PORT, 60000),
    ]);
    console.log('Both servers ready.\n');

    // ─────────────────────────────────────────────────────────────────────
    // FIXTURE SETUP
    // Fixture 1  — API tests (AT-1 … AT-9 + listing1 collateral check)
    // Fixture 2  — Browser tests (isolated: zero Telegram bindings before BT-0)
    // ─────────────────────────────────────────────────────────────────────
    console.log('── Fixtures ────────────────────────────────────────────────');
    const { token: tok1, listingId: lid1 } = await createFixture(BACKEND_URL, WALLET1);
    await awaitActive(BACKEND_URL, lid1);
    const pid1 = db(dbPath, `SELECT owner_principal_id FROM listings WHERE id='${lid1}'`);
    assert(pid1.startsWith('prn_'), `fixture1 principal set (${pid1})`);
    console.log(`  listing1=${lid1}  principal1=${pid1}`);

    const { token: tok2, listingId: lid2 } = await createFixture(BACKEND_URL, WALLET2);
    await awaitActive(BACKEND_URL, lid2);
    const pid2 = db(dbPath, `SELECT owner_principal_id FROM listings WHERE id='${lid2}'`);
    assert(pid2.startsWith('prn_'), `fixture2 principal set (${pid2})`);
    console.log(`  listing2=${lid2}  principal2=${pid2}`);

    // Hard pre-condition: listing2 must have zero Telegram bindings before any BT.
    const preBind = db(dbPath,
      `SELECT COUNT(*) FROM client_listing_notifications WHERE listing_id='${lid2}'`);
    assert(preBind === '0', `listing2 has 0 bindings before browser tests (got ${preBind})`);

    // ─────────────────────────────────────────────────────────────────────
    // AT-1: Two concurrent token requests → exactly one live token
    // ─────────────────────────────────────────────────────────────────────
    console.log('\n── AT-1: concurrent issuance ───────────────────────────────');
    {
      const [r1, r2] = await Promise.all([
        apiCall(BACKEND_URL, 'POST', '/telegram/client/token', { listing_id: lid1 }, tok1),
        apiCall(BACKEND_URL, 'POST', '/telegram/client/token', { listing_id: lid1 }, tok1),
      ]);
      assert(r1.status === 201, `AT-1: concurrent req 1 → 201 (got ${r1.status})`);
      assert(r2.status === 201, `AT-1: concurrent req 2 → 201 (got ${r2.status})`);
      const live = db(dbPath,
        `SELECT COUNT(*) FROM telegram_link_tokens WHERE listing_id='${lid1}' AND token_type='client' AND used=0`);
      assert(live === '1', `AT-1: exactly 1 live token after concurrent issuance (got ${live})`);
    }

    // ─────────────────────────────────────────────────────────────────────
    // AT-2: Issuance rollback — SQLite BEFORE INSERT trigger
    // ─────────────────────────────────────────────────────────────────────
    console.log('\n── AT-2: issuance rollback (SQLite trigger) ────────────────');
    {
      // Issue and record exactly one live token.
      const tr = await apiCall(BACKEND_URL, 'POST', '/telegram/client/token',
        { listing_id: lid1 }, tok1);
      assert(tr.status === 201, `AT-2 setup: issue live token → 201 (got ${tr.status})`);
      const savedToken = tr.body.token;

      const usedPre = db(dbPath,
        `SELECT used FROM telegram_link_tokens WHERE token='${savedToken}'`);
      assert(usedPre === '0', `AT-2 setup: saved token is unused (got ${usedPre})`);

      // Install a schema-level trigger (visible to all connections, including the backend's).
      // RAISE(ABORT) aborts the statement and rolls back the enclosing transaction.
      db(dbPath,
        `CREATE TRIGGER _e2e_abort_insert BEFORE INSERT ON telegram_link_tokens ` +
        `BEGIN SELECT RAISE(ABORT, 'e2e injected failure'); END`);

      // Request a replacement. Inside TelegramClientToken:
      //   BEGIN; UPDATE saved_token → used=1; INSERT new_token → ABORT (trigger);
      //   ROLLBACK → UPDATE undone → saved_token back to used=0.
      // The handler's INSERT-error branch calls writeError(w, 500, "db error").
      const rr = await apiCall(BACKEND_URL, 'POST', '/telegram/client/token',
        { listing_id: lid1 }, tok1);
      assert(rr.status === 500, `AT-2: failed INSERT → HTTP 500 (got ${rr.status})`);

      // Verify rollback: saved token must still be unused and unexpired.
      const usedAfter = db(dbPath,
        `SELECT used FROM telegram_link_tokens WHERE token='${savedToken}'`);
      assert(usedAfter === '0',
        `AT-2: previous token rolled back to used=0 (got ${usedAfter})`);

      const expAfter = parseInt(db(dbPath,
        `SELECT expires_at FROM telegram_link_tokens WHERE token='${savedToken}'`), 10);
      const nowSec = Math.floor(Date.now() / 1000);
      assert(expAfter > nowSec,
        `AT-2: previous token is still unexpired (expires_at=${expAfter}, now=${nowSec})`);

      // Remove the trigger — normal operations must resume.
      db(dbPath, `DROP TRIGGER _e2e_abort_insert`);

      // The saved token must be consumable after rollback.
      const wbr = await webhook(BACKEND_URL, savedToken, CHAT_C);
      assert(wbr.status === 200 && wbr.body.status === 'ok',
        `AT-2: rolled-back token still consumable → 200 ok (got ${wbr.status})`);
    }

    // ─────────────────────────────────────────────────────────────────────
    // AT-3: Null-principal token rejected by webhook; claim rolled back
    // ─────────────────────────────────────────────────────────────────────
    console.log('\n── AT-3: null-principal token rejection ────────────────────');
    {
      const nowSec = Math.floor(Date.now() / 1000);
      db(dbPath,
        `INSERT INTO telegram_link_tokens ` +
        `(id, token, token_type, listing_id, principal_id, created_at, expires_at, used) ` +
        `VALUES ('tgl_null047', 'null-princ-047', 'client', '${lid1}', NULL, ` +
        `${nowSec}, ${nowSec + 600}, 0)`);

      const wr = await webhook(BACKEND_URL, 'null-princ-047', CHAT_A);
      assert(wr.status === 400, `AT-3: null-principal webhook → 400 (got ${wr.status})`);

      const used = db(dbPath,
        `SELECT used FROM telegram_link_tokens WHERE token='null-princ-047'`);
      assert(used === '0', `AT-3: token claim rolled back to used=0 (got ${used})`);
    }

    // ─────────────────────────────────────────────────────────────────────
    // AT-4: Active listing with expired visible_until rejects issuance (409)
    // ─────────────────────────────────────────────────────────────────────
    console.log('\n── AT-4: expired visible_until → issuance 409 ──────────────');
    {
      // Save original value — restore it, never use a hardcoded offset.
      const origVU4 = db(dbPath, `SELECT visible_until FROM listings WHERE id='${lid1}'`);

      db(dbPath,
        `UPDATE listings SET visible_until=${Math.floor(Date.now() / 1000) - 5} ` +
        `WHERE id='${lid1}'`);

      const er = await apiCall(BACKEND_URL, 'POST', '/telegram/client/token',
        { listing_id: lid1 }, tok1);
      assert(er.status === 409, `AT-4: expired visible_until → 409 (got ${er.status})`);

      // Restore the original visible_until.
      db(dbPath, `UPDATE listings SET visible_until=${origVU4} WHERE id='${lid1}'`);
    }

    // ─────────────────────────────────────────────────────────────────────
    // AT-5: Expired visible_until rejects webhook; token claim rolled back
    // ─────────────────────────────────────────────────────────────────────
    console.log('\n── AT-5: expired visible_until → webhook 400 + rollback ────');
    {
      // Issue a valid token while visible_until is restored.
      const tr5 = await apiCall(BACKEND_URL, 'POST', '/telegram/client/token',
        { listing_id: lid1 }, tok1);
      assert(tr5.status === 201, `AT-5 setup: token issued → 201 (got ${tr5.status})`);
      const t5 = tr5.body.token;

      // Save then expire visible_until.
      const origVU5 = db(dbPath, `SELECT visible_until FROM listings WHERE id='${lid1}'`);
      db(dbPath,
        `UPDATE listings SET visible_until=${Math.floor(Date.now() / 1000) - 1} ` +
        `WHERE id='${lid1}'`);

      const wr5 = await webhook(BACKEND_URL, t5, CHAT_A);
      assert(wr5.status === 400, `AT-5: expired-listing webhook → 400 (got ${wr5.status})`);

      const used5 = db(dbPath,
        `SELECT used FROM telegram_link_tokens WHERE token='${t5}'`);
      assert(used5 === '0',
        `AT-5: token claim rolled back to used=0 (got ${used5})`);

      // Restore the original visible_until.
      db(dbPath, `UPDATE listings SET visible_until=${origVU5} WHERE id='${lid1}'`);
    }

    // ─────────────────────────────────────────────────────────────────────
    // AT-6/7/8: Old-token rejection, fresh consumption, replay rejection
    // ─────────────────────────────────────────────────────────────────────
    console.log('\n── AT-6/7/8: old token / fresh token / replay ──────────────');
    let tOld, tFresh;
    {
      // Issue first token.
      const ra = await apiCall(BACKEND_URL, 'POST', '/telegram/client/token',
        { listing_id: lid1 }, tok1);
      assert(ra.status === 201, `AT-6: first token → 201 (got ${ra.status})`);
      tOld = ra.body.token;
      assert(ra.body.expires_in === 600, `AT-6: expires_in=600 (got ${ra.body.expires_in})`);

      // Issue replacement — atomically invalidates tOld.
      const rb = await apiCall(BACKEND_URL, 'POST', '/telegram/client/token',
        { listing_id: lid1 }, tok1);
      assert(rb.status === 201, `AT-6: fresh token → 201 (got ${rb.status})`);
      tFresh = rb.body.token;
      assert(tFresh !== tOld, `AT-6: fresh token differs from old`);

      const oldUsed = db(dbPath,
        `SELECT used FROM telegram_link_tokens WHERE token='${tOld}'`);
      assert(oldUsed === '1', `AT-6: old token marked used=1 (got ${oldUsed})`);

      // AT-6: old (invalidated) token rejected.
      const wr6 = await webhook(BACKEND_URL, tOld, CHAT_A);
      assert(wr6.status === 400, `AT-6: old token webhook → 400 (got ${wr6.status})`);

      // AT-7: fresh token consumed.
      const wr7 = await webhook(BACKEND_URL, tFresh, CHAT_A);
      assert(wr7.status === 200 && wr7.body.status === 'ok',
        `AT-7: fresh token webhook → 200 ok (got ${wr7.status})`);

      // AT-8: replay rejected.
      const wr8 = await webhook(BACKEND_URL, tFresh, CHAT_B);
      assert(wr8.status === 400, `AT-8: replay webhook → 400 (got ${wr8.status})`);

      // Exactly one active binding.
      const bc = db(dbPath,
        `SELECT COUNT(*) FROM client_listing_notifications WHERE listing_id='${lid1}' AND active=1`);
      assert(bc === '1', `AT-8: exactly 1 active binding (got ${bc})`);
    }

    // ─────────────────────────────────────────────────────────────────────
    // AT-9: Second webhook replaces binding; old chat_id deactivated
    // ─────────────────────────────────────────────────────────────────────
    console.log('\n── AT-9: second webhook replaces binding ───────────────────');
    {
      const r9 = await apiCall(BACKEND_URL, 'POST', '/telegram/client/token',
        { listing_id: lid1 }, tok1);
      assert(r9.status === 201, `AT-9: token → 201 (got ${r9.status})`);
      const t9 = r9.body.token;

      const wr9 = await webhook(BACKEND_URL, t9, CHAT_B);
      assert(wr9.status === 200, `AT-9: second webhook → 200 (got ${wr9.status})`);

      const total = db(dbPath,
        `SELECT COUNT(*) FROM client_listing_notifications WHERE listing_id='${lid1}' AND active=1`);
      assert(total === '1', `AT-9: exactly 1 active binding after replacement (got ${total})`);

      const oldChatActive = db(dbPath,
        `SELECT COUNT(*) FROM client_listing_notifications ` +
        `WHERE listing_id='${lid1}' AND telegram_chat_id='${CHAT_A}' AND active=1`);
      assert(oldChatActive === '0', `AT-9: old CHAT_A binding deactivated (got ${oldChatActive})`);
    }

    // ─────────────────────────────────────────────────────────────────────
    // Listing1 collateral check (AT tests must not create extra records)
    // ─────────────────────────────────────────────────────────────────────
    console.log('\n── Listing1 collateral check ───────────────────────────────');
    {
      const lc = db(dbPath,
        `SELECT COUNT(*) FROM listings WHERE owner_principal_id='${pid1}'`);
      assert(lc === '1', `listing1: still exactly 1 listing (got ${lc})`);
      const ic = db(dbPath,
        `SELECT COUNT(*) FROM invoices WHERE listing_id='${lid1}'`);
      assert(ic === '1', `listing1: still exactly 1 invoice (got ${ic})`);
      const oc = db(dbPath,
        `SELECT opened_chats_count FROM listings WHERE id='${lid1}'`);
      assert(oc === '0', `listing1: opened_chats_count=0 (got ${oc})`);
    }

    // ─────────────────────────────────────────────────────────────────────
    // BROWSER TESTS — listing2 is the isolated fixture, no prior binding
    // ─────────────────────────────────────────────────────────────────────
    console.log('\n── BT-0: issue initial token, expire it, webhook 400 ───────');
    let expiredTok2;
    {
      // Issue an initial token for listing2.
      const btr = await apiCall(BACKEND_URL, 'POST', '/telegram/client/token',
        { listing_id: lid2 }, tok2);
      assert(btr.status === 201, `BT-0: initial token → 201 (got ${btr.status})`);
      expiredTok2 = btr.body.token;

      // Set its expires_at to the past — simulates user never clicking the bot link.
      db(dbPath,
        `UPDATE telegram_link_tokens SET expires_at=${Math.floor(Date.now() / 1000) - 5} ` +
        `WHERE token='${expiredTok2}'`);

      // The expired token must be rejected by the webhook.
      const wrExp = await webhook(BACKEND_URL, expiredTok2, CHAT_D);
      assert(wrExp.status === 400, `BT-0: expired-token webhook → 400 (got ${wrExp.status})`);

      // No binding must have been created (expired token is rejected before binding).
      const bindExp = db(dbPath,
        `SELECT COUNT(*) FROM client_listing_notifications WHERE listing_id='${lid2}' AND active=1`);
      assert(bindExp === '0', `BT-0: no binding after expired-token rejection (got ${bindExp})`);
    }

    // ─────────────────────────────────────────────────────────────────────
    // BT-1/2/3: Navigate away → return → reload → Connect → Connected
    // This reproduces the exact production regression: user left the /new
    // Telegram step, returned to their listing, and found no way to reconnect.
    // ─────────────────────────────────────────────────────────────────────
    console.log('\n── BT-1/2/3: navigate-away → return → connect → connected ─');
    browser = await chromium.launch({ headless: true });
    {
      const ctx  = await browser.newContext();
      const page = await ctx.newPage();

      // Inject the listing2 owner session into sessionStorage before any navigation.
      // addInitScript runs in page JS context before page scripts — sessionStorage is accessible.
      await page.addInitScript(({ key, value }) => {
        window.sessionStorage.setItem(key, value);
      }, { key: 'naroom_session_client', value: tok2 });

      // Navigate away (simulates user on a different page after failing to complete flow).
      await page.goto(`${FRONTEND_URL}/board/tbilisi`, { waitUntil: 'domcontentloaded' });
      await page.waitForTimeout(500);

      // Return to the exact listing route.
      await page.goto(`${FRONTEND_URL}/listing/${lid2}`, { waitUntil: 'domcontentloaded' });

      // Reload the page (simulates reopening the listing).
      await page.reload({ waitUntil: 'domcontentloaded' });
      // Allow onMount + checkOwnerTelegram + confirm API call to complete.
      await page.waitForTimeout(3000);

      // BT-1: Telegram section must be visible after reload.
      const tgSection = page.locator('.section', { hasText: /telegram/i });
      if (!await tgSection.isVisible().catch(() => false)) {
        const body = await page.evaluate(() => document.body.innerText).catch(() => '');
        console.log('  [debug] page body:', body.slice(0, 500).replace(/\n/g, ' '));
        fail('BT-1: Telegram section not visible after navigate-away → return → reload');
      }
      pass('BT-1: Telegram section visible after returning to listing');

      // BT-2: Connect Telegram button must be visible.
      // An already-connected state is a hard test failure: the fixture must have no binding.
      const connectedBox = page.locator('.tg-connected');
      if (await connectedBox.isVisible().catch(() => false)) {
        fail('BT-2: listing2 already shows Connected — fixture must have no active binding before BT-1');
      }
      const connectBtn = page.locator('button.btn-primary', { hasText: /connect telegram/i }).first();
      if (!await connectBtn.isVisible().catch(() => false)) {
        const body = await page.evaluate(() => document.body.innerText).catch(() => '');
        console.log('  [debug] page body:', body.slice(0, 500).replace(/\n/g, ' '));
        fail('BT-2: Connect Telegram button not visible — expected disconnected state');
      }
      pass('BT-2: Connect Telegram button visible (confirmed: no prior binding)');

      // BT-3: Click "Connect Telegram" — a fresh t.me deep link must appear.
      await connectBtn.click();
      await page.waitForTimeout(2000);

      const botLink = page.locator('a.btn-primary[href*="t.me"]');
      if (!await botLink.isVisible().catch(() => false)) {
        const body = await page.evaluate(() => document.body.innerText).catch(() => '');
        console.log('  [debug] page body:', body.slice(0, 500).replace(/\n/g, ' '));
        fail('BT-3: fresh t.me deep link not rendered after clicking Connect');
      }
      pass('BT-3: fresh t.me deep link rendered after clicking Connect');

      // Read the new live token from the DB (the click just issued it).
      const newTok2 = db(dbPath,
        `SELECT token FROM telegram_link_tokens ` +
        `WHERE listing_id='${lid2}' AND token_type='client' AND used=0 ` +
        `ORDER BY created_at DESC LIMIT 1`);
      assert(newTok2.length > 0, `BT-3: fresh token found in DB`);

      // Consume the fresh token via webhook.
      const wrBT = await webhook(BACKEND_URL, newTok2, CHAT_D);
      assert(wrBT.status === 200 && wrBT.body.status === 'ok',
        `BT-3: webhook consumed fresh token → 200 ok (got ${wrBT.status})`);

      // Wait up to 12 s for the 3-second poll to detect Connected.
      let connected = false;
      const pollDeadline = Date.now() + 12000;
      while (Date.now() < pollDeadline) {
        await page.waitForTimeout(1000);
        if (await connectedBox.isVisible().catch(() => false)) { connected = true; break; }
      }
      if (!connected) {
        const body = await page.evaluate(() => document.body.innerText).catch(() => '');
        console.log('  [debug] page body:', body.slice(0, 300).replace(/\n/g, ' '));
        fail('BT-3: page did not render Connected within 12 s of webhook consumption');
      }
      pass('BT-3: page renders Connected after webhook consumed token');

      await ctx.close();
    }

    // ─────────────────────────────────────────────────────────────────────
    // BT-4: Non-owner (listing1's session) sees no Telegram section on listing2
    // ─────────────────────────────────────────────────────────────────────
    console.log('\n── BT-4: non-owner sees no Telegram section ────────────────');
    {
      const ctx  = await browser.newContext();
      const page = await ctx.newPage();

      // tok1 owns listing1 — it is not the owner of listing2.
      await page.addInitScript(({ key, value }) => {
        window.sessionStorage.setItem(key, value);
      }, { key: 'naroom_session_client', value: tok1 });

      await page.goto(`${FRONTEND_URL}/listing/${lid2}`, { waitUntil: 'domcontentloaded' });
      await page.waitForTimeout(2500);

      // Non-owner: confirm returns 403 → ownerTgState = 'not_owner' → section hidden.
      const visible = await page.locator('.section', { hasText: /telegram/i }).isVisible().catch(() => false);
      assert(!visible, 'BT-4: non-owner sees no Telegram section');

      await ctx.close();
    }

    // ─────────────────────────────────────────────────────────────────────
    // BT-5: Direct API access control
    // ─────────────────────────────────────────────────────────────────────
    console.log('\n── BT-5: API access control ────────────────────────────────');
    {
      const r401 = await fetch(`${BACKEND_URL}/telegram/client/confirm?listing_id=${lid2}`);
      assert(r401.status === 401,
        `BT-5: unauthenticated confirm → 401 (got ${r401.status})`);

      // A fresh session with no wallet is a valid principal but not the listing owner.
      const srOut = await apiCall(BACKEND_URL, 'POST', '/session/init', { role: 'client' });
      const outTok = srOut.body.session_token;
      const r403 = await apiCall(BACKEND_URL, 'GET',
        `/telegram/client/confirm?listing_id=${lid2}`, undefined, outTok);
      assert(r403.status === 403, `BT-5: non-owner confirm → 403 (got ${r403.status})`);
    }

    // ─────────────────────────────────────────────────────────────────────
    // BT-6: No collateral damage on listing2
    // ─────────────────────────────────────────────────────────────────────
    console.log('\n── BT-6: listing2 collateral check ─────────────────────────');
    {
      const lc = db(dbPath, `SELECT COUNT(*) FROM listings WHERE owner_principal_id='${pid2}'`);
      assert(lc === '1', `BT-6: exactly 1 listing2 (got ${lc})`);
      const ic = db(dbPath, `SELECT COUNT(*) FROM invoices WHERE listing_id='${lid2}'`);
      assert(ic === '1', `BT-6: exactly 1 invoice for listing2 (got ${ic})`);
      const oc = db(dbPath, `SELECT opened_chats_count FROM listings WHERE id='${lid2}'`);
      assert(oc === '0', `BT-6: opened_chats_count=0 (got ${oc})`);
      const rc = db(dbPath, `SELECT COUNT(*) FROM responses WHERE listing_id='${lid2}'`);
      assert(rc === '0', `BT-6: no responses for listing2 (got ${rc})`);
      const crc = db(dbPath, `SELECT COUNT(*) FROM chat_rooms WHERE listing_id='${lid2}'`);
      assert(crc === '0', `BT-6: no chat rooms for listing2 (got ${crc})`);
      const bindAfter = db(dbPath,
        `SELECT COUNT(*) FROM client_listing_notifications WHERE listing_id='${lid2}' AND active=1`);
      assert(bindAfter === '1',
        `BT-6: exactly 1 active binding after reconnect flow (got ${bindAfter})`);
    }

  } finally {
    if (browser) await browser.close().catch(() => {});
    cleanupResult = await cleanup();
  }

  // ── Final summary ──────────────────────────────────────────────────────────
  console.log('\n────────────────────────────────────────────────────');
  console.log(`  Passed:  ${passed}`);
  console.log(`  Failed:  ${failed}`);
  console.log(`  backend  port ${BACKEND_PORT}: closed=${cleanupResult.backendClosed} exited=${cleanupResult.backendExited}`);
  console.log(`  frontend port ${FRONTEND_PORT}: closed=${cleanupResult.frontendClosed} exited=${cleanupResult.viteExited}`);
  if (cleanupResult.strays) {
    console.error(`  WARNING: stray processes: ${cleanupResult.strays}`);
  } else {
    console.log('  No stray backend binary processes');
  }
  console.log('────────────────────────────────────────────────────');

  if (failed > 0) throw new Error(`${failed} test(s) failed`);
  if (!cleanupResult.backendClosed || !cleanupResult.frontendClosed) {
    throw new Error('CLEANUP FAILURE: at least one port is still open');
  }
  if (cleanupResult.strays) {
    throw new Error(`CLEANUP FAILURE: stray backend binary processes found: ${cleanupResult.strays}`);
  }

  console.log('\n✓ 047_telegram_reconnect: all tests passed\n');
}

run().catch(e => {
  console.error('\nTest failed:', e.message);
  process.exit(1);
});
