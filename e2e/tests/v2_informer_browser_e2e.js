#!/usr/bin/env node
/**
 * V2 Informer Browser E2E
 *
 * Starts V2 backend + Vite frontend on dynamic ports, exercises the full
 * Informer flow through the real browser UI + real V2 Client HTTP endpoints:
 *
 *   Step 1 — Set $2000 balance for informer wallet.
 *   Step 2 — Navigate to /v2/informer, fill wallet+city(tbilisi), click check
 *             → "waiting" state appears; capture raw_token from API response.
 *   Step 3 — Simulate Telegram /start → UI transitions to "connected".
 *   Step 4 — Real Client first publish for tbilisi via V2 API:
 *             payment-intents → dev/payment/confirm → dev/telegram/connect
 *             → listings/publish → dev/informer/run-worker.
 *             Verifies notification delivered and references "tbilisi" with
 *             listing URL (/v2/listing/<id>).
 *   Step 5 — Real reactivation: dev/listing/expire → dev/telegram/connect
 *             → listings/reactivate → run-worker → verifies 0 notifications
 *             (reactivation must NOT create a new outbox event).
 *   Step 6 — City change tbilisi → batumi: same wallet, new city, new token.
 *             UI enters "waiting", simulate-start, UI shows "connected".
 *   Step 7 — After city change: real tbilisi publish → 0 notifications;
 *             real batumi publish → 1 notification referencing "batumi".
 *   Step 8 — /stop: send webhook /stop; subsequent real batumi publish delivers
 *             0 notifications.
 *
 * Two sequential runs (0 FAIL, 0 SKIP required).
 *
 * Dev endpoints allowed:
 *   - /dev/balance/set              (controlled balance for informer eligibility)
 *   - /dev/payment/confirm          (simulated blockchain payment confirmation)
 *   - /dev/telegram/connect         (simulated Telegram binding — client bot)
 *   - /dev/informer/simulate-start  (simulated Telegram /start — informer bot)
 *   - /dev/informer/run-worker      (trigger worker for E2E-controlled delivery)
 *   - /dev/listing/expire           (force listing to expire for reactivation test)
 *   - /dev/informer/notifications   (capture delivered notifications)
 *   - /v2/telegram/informer/webhook (direct /stop simulation)
 *   - /dev/informer/fake-first-publish is NOT used for domain publication events.
 *
 * Domain publication events (first publish, reactivation) go through:
 *   POST /v2/client/payment-intents
 *   POST /v2/client/listings/publish
 *   POST /v2/client/listings/reactivate
 */

import { chromium } from 'playwright';
import { spawn, execSync } from 'child_process';
import { mkdtempSync, mkdirSync, existsSync } from 'fs';
import { tmpdir } from 'os';
import { join, resolve } from 'path';
import net from 'net';

const ROOT = resolve(new URL('../../', import.meta.url).pathname);
const SCREENSHOTS_DIR = join(ROOT, 'e2e', 'screenshots');
if (!existsSync(SCREENSHOTS_DIR)) mkdirSync(SCREENSHOTS_DIR, { recursive: true });

// Build dev binary once.
const DEV_BINARY = join(tmpdir(), 'naroom-v2-informer-e2e');
console.log('  Building V2 dev binary...');
execSync(`go build -o ${DEV_BINARY} ./cmd/naroom-v2-dev/`, {
  cwd: ROOT,
  stdio: ['ignore', 'ignore', 'inherit'],
  timeout: 120000,
});
console.log(`  ✓ Binary: ${DEV_BINARY}`);

// ── Constants ──────────────────────────────────────────────────────────────────

// Valid mainnet BTC bech32 P2WPKH address (checksum-valid).
// Used for both informer eligibility check and Client payment flow.
const INFORMER_WALLET = 'bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq';

// Must match devInformerWebhookSecret in cmd/naroom-v2-dev/main.go
const DEV_INFORMER_WEBHOOK_SECRET = 'dev-informer-webhook-secret-v200';

// Must match devInformerFakeChatID in cmd/naroom-v2-dev/main.go
const DEV_INFORMER_FAKE_CHAT_ID = 987654321;

// ── Helpers ────────────────────────────────────────────────────────────────────

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

async function findFreePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.listen(0, '127.0.0.1', () => {
      const port = srv.address().port;
      srv.close(() => resolve(port));
    });
    srv.on('error', reject);
  });
}

async function isPortOpen(port) {
  async function tryHost(host) {
    return new Promise(resolve => {
      const c = net.createConnection(port, host);
      c.setTimeout(500);
      c.on('connect', () => { c.destroy(); resolve(true); });
      c.on('error', () => resolve(false));
      c.on('timeout', () => { c.destroy(); resolve(false); });
    });
  }
  return (await tryHost('127.0.0.1')) || (await tryHost('::1'));
}

async function waitForPort(port, { timeout = 30000, label = '' } = {}) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (await isPortOpen(port)) return;
    await sleep(500);
  }
  throw new Error(`Port ${port} (${label}) did not open within ${timeout}ms`);
}

async function assertPortClosed(port, { timeout = 5000 } = {}) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (!await isPortOpen(port)) return;
    await sleep(200);
  }
  throw new Error(`Port ${port} still open after ${timeout}ms — teardown FAILED`);
}

// devAPI: calls backend directly (simulates external events — blockchain/Telegram).
// Allowed for: balance, payment confirmation, Telegram capture, worker trigger,
//              listing expiry, notification capture.
// NOT allowed for: replacing domain publication events (fake-first-publish).
async function devAPI(base, method, path, body) {
  const res = await fetch(`${base}${path}`, {
    method,
    headers: { 'Content-Type': 'application/json' },
    ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
  });
  const text = await res.text();
  let json = null;
  try { json = JSON.parse(text); } catch {}
  if (!res.ok) throw new Error(`devAPI ${method} ${path} → ${res.status}: ${text}`);
  return json;
}

/**
 * fullClientPublish performs a complete first-publish flow via real V2 Client endpoints:
 *   1. POST /v2/client/payment-intents            (real domain endpoint)
 *   2. POST /dev/payment/confirm                  (dev — payment simulation)
 *   3. POST /dev/telegram/connect                 (dev — Telegram binding)
 *   4. POST /v2/client/listings/publish           (real domain endpoint)
 *
 * Returns { managementCode, flowId, listingId }.
 *
 * Callers must run the worker separately via /dev/informer/run-worker.
 */
async function fullClientPublish(backendBase, walletAddress, city) {
  // 1. Create payment intent (real endpoint).
  const pi = await devAPI(backendBase, 'POST', '/v2/client/payment-intents', {
    wallet_address: walletAddress,
  });
  const managementCode = pi.management_code;
  const flowId = pi.flow_id;

  // 2. Confirm payment (dev endpoint — simulates blockchain detection).
  await devAPI(backendBase, 'POST', '/dev/payment/confirm', {
    flow_id: flowId,
    wallet_address: walletAddress,
  });

  // 3. Connect Telegram (dev endpoint — simulates Telegram bot binding).
  await devAPI(backendBase, 'POST', '/dev/telegram/connect', {
    management_code: managementCode,
    wallet_address: walletAddress,
  });

  // 4. Publish listing (real domain endpoint — triggers EnqueueFirstPublishTx atomically).
  const listing = await devAPI(backendBase, 'POST', '/v2/client/listings/publish', {
    management_code: managementCode,
    wallet_address: walletAddress,
    city,
    dependency_type: 'alcohol',
    help_type: 'crisis',
    urgency: 'urgent',
    languages: ['en'],
    contact_type: 'telegram',
    contact: '@e2etestinformer',
  });

  return { managementCode, flowId, listingId: listing.id };
}

// Simulate Telegram /stop via direct webhook call (no dedicated dev endpoint).
async function telegramStop(backendBase, chatId = DEV_INFORMER_FAKE_CHAT_ID) {
  const res = await fetch(`${backendBase}/v2/telegram/informer/webhook`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-Telegram-Bot-Api-Secret-Token': DEV_INFORMER_WEBHOOK_SECRET,
    },
    body: JSON.stringify({
      update_id: Math.floor(Math.random() * 999999),
      message: {
        message_id: Math.floor(Math.random() * 9999),
        chat: { id: chatId, type: 'private' },
        text: '/stop',
      },
    }),
  });
  return res.status;
}

function assert(cond, msg) {
  if (!cond) throw new Error(msg);
}

/**
 * watchForClaimedStatus sets up a page.on('response') listener that watches
 * for the browser's /api/v2/informer/status poll returning state="claimed".
 * Returns { stop, waitClaimed } where:
 *   stop() — removes the listener
 *   waitClaimed(ms) — resolves when claimed is seen, or rejects on timeout
 */
function watchForClaimedStatus(page) {
  let claimedResolve = null;
  let claimed = false;
  const claimedPromise = new Promise(resolve => { claimedResolve = resolve; });

  const handler = async (resp) => {
    if (claimed) return;
    if (!resp.url().includes('/informer/status')) return;
    if (resp.request().method() !== 'POST') return;
    try {
      const body = await resp.json();
      if (body?.state === 'claimed') {
        claimed = true;
        claimedResolve();
      }
    } catch {}
  };
  page.on('response', handler);

  return {
    stop: () => page.off('response', handler),
    waitClaimed: (ms = 20000) => {
      return Promise.race([
        claimedPromise,
        sleep(ms).then(() => {
          if (!claimed) throw new Error(`status never became "claimed" within ${ms}ms`);
        }),
      ]);
    },
  };
}

// ── Process management ─────────────────────────────────────────────────────────

async function startTestEnv() {
  const backendPort = await findFreePort();
  const frontendPort = await findFreePort();
  const tmpDb = join(mkdtempSync(join(tmpdir(), 'v2-informer-e2e-')), 'naroom-v2.db');

  const backend = spawn(DEV_BINARY, [], {
    cwd: ROOT,
    env: { ...process.env, DEV_PORT: String(backendPort), DEV_DB_PATH: tmpDb },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  backend._lines = [];
  backend.stdout.on('data', d => backend._lines.push(...d.toString().split('\n')));
  backend.stderr.on('data', d => backend._lines.push(...d.toString().split('\n')));
  await waitForPort(backendPort, { label: 'backend', timeout: 15000 });

  const viteEntry = join(ROOT, 'frontend', 'node_modules', 'vite', 'bin', 'vite.js');
  const frontend = spawn(
    process.execPath,
    [viteEntry, 'dev', '--port', String(frontendPort)],
    {
      cwd: join(ROOT, 'frontend'),
      env: {
        ...process.env,
        BACKEND_URL_V2: `http://127.0.0.1:${backendPort}`,
        BACKEND_URL: `http://127.0.0.1:${backendPort}`,
        NO_COLOR: '1',
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    }
  );
  frontend._lines = [];
  frontend.stdout.on('data', d => frontend._lines.push(...d.toString().split('\n')));
  frontend.stderr.on('data', d => frontend._lines.push(...d.toString().split('\n')));
  await waitForPort(frontendPort, { label: 'frontend', timeout: 60000 });

  const backendBase = `http://127.0.0.1:${backendPort}`;
  const frontendBase = `http://localhost:${frontendPort}`;
  console.log(`  backend:  ${backendBase}`);
  console.log(`  frontend: ${frontendBase}`);

  return { backend, frontend, backendBase, frontendBase, backendPort, frontendPort };
}

async function teardown(env) {
  try { env.backend.kill('SIGKILL'); } catch {}
  try { env.frontend.kill('SIGKILL'); } catch {}
  await sleep(1500);
  await assertPortClosed(env.backendPort, { timeout: 5000 });
  await assertPortClosed(env.frontendPort, { timeout: 5000 });
  console.log(`  ✓ ports ${env.backendPort} and ${env.frontendPort} released`);
}

// ── Single run ─────────────────────────────────────────────────────────────────

async function runOnce(runNumber) {
  console.log(`\n  ── Informer Run ${runNumber} ─────────────────────────────────`);
  const env = await startTestEnv();
  const { backendBase, frontendBase } = env;

  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext();

  let passed = 0;
  let failed = 0;
  const failures = [];

  async function step(name, fn) {
    try {
      await fn();
      console.log(`    ✓ ${name}`);
      passed++;
    } catch (e) {
      console.error(`    ✗ ${name}: ${e.message}`);
      failed++;
      failures.push({ name, error: e.message });
    }
  }

  // State across steps.
  let rawToken = '';      // tbilisi informer token
  let rawToken2 = '';    // batumi informer token (step 6)
  let tbilisiListing = null; // { managementCode, flowId, listingId } from step 4
  let page = null;

  try {

    // ── Step 1: Set $2000 balance ────────────────────────────────────────────
    await step('1: Set $2000 balance for informer wallet', async () => {
      await devAPI(backendBase, 'POST', '/dev/balance/set', {
        wallet_address: INFORMER_WALLET,
        balance_usd: 2000.0,
      });
    });

    // ── Step 2: Navigate to /v2/informer → fill form → waiting state ─────────
    await step('2: Wallet+city(tbilisi) form → waiting state, capture raw_token', async () => {
      page = await context.newPage();
      page.on('popup', p => { p.close().catch(() => {}); });
      await page.setViewportSize({ width: 1440, height: 900 });

      // Set up response capture BEFORE navigation so we don't miss the response.
      const accessPromise = page.waitForResponse(
        r => r.url().includes('/v2/informer/access') && r.request().method() === 'POST',
        { timeout: 20000 }
      );

      await page.goto(`${frontendBase}/v2/informer`);
      await page.waitForLoadState('domcontentloaded');

      // Fill wallet.
      await page.fill('input[type="text"]', INFORMER_WALLET);
      await page.waitForTimeout(300);

      // Select city tbilisi (default, but explicit).
      await page.selectOption('select', 'tbilisi');

      // Click check button.
      const btn = page.locator('button.btn-primary:not(:disabled)').first();
      await btn.waitFor({ timeout: 8000 });
      await btn.click();

      // Capture raw_token from API response.
      const accessResp = await accessPromise;
      assert(accessResp.status() === 200, `access returned ${accessResp.status()}`);
      const accessData = await accessResp.json();
      rawToken = accessData.raw_token;
      assert(rawToken && rawToken.length > 0, 'raw_token missing from access response');

      // UI must show waiting state.
      await page.waitForSelector('.status-badge.waiting', { timeout: 12000 });

      await page.screenshot({ path: join(SCREENSHOTS_DIR, `inf_r${runNumber}_s2_waiting.png`) });
    });

    // ── Step 3: Simulate Telegram /start → UI transitions to connected ───────
    await step('3: Simulate Telegram /start → connected state', async () => {
      assert(rawToken, 'rawToken not set from step 2');

      // Watch for the status poll to return "claimed".
      const watcher = watchForClaimedStatus(page);

      // Simulate /start via dev endpoint (Telegram capture — allowed).
      await devAPI(backendBase, 'POST', '/dev/informer/simulate-start', { raw_token: rawToken });

      // Wait for browser status poll to detect "claimed".
      await watcher.waitClaimed(18000);
      watcher.stop();

      // DOM should update to show connected.
      await page.waitForSelector('.done-icon', { timeout: 8000 });

      await page.screenshot({ path: join(SCREENSHOTS_DIR, `inf_r${runNumber}_s3_connected.png`) });
    });

    // ── Step 4: Real Client first publish → notification delivered ────────────
    // Uses real V2 Client domain endpoints (not fake-first-publish).
    // Proves: real listing tx → atomic outbox enqueue → worker → notification.
    await step('4: Real Client first publish → notification delivered with listing URL', async () => {
      // Drain any stale notifications first.
      await devAPI(backendBase, 'GET', '/dev/informer/notifications');

      // Full Client publish flow via real domain endpoints.
      tbilisiListing = await fullClientPublish(backendBase, INFORMER_WALLET, 'tbilisi');
      assert(tbilisiListing.listingId, 'listingId missing from publish response');

      // Trigger informer worker (dev endpoint — allowed for E2E-controlled delivery).
      await devAPI(backendBase, 'POST', '/dev/informer/run-worker');

      // Verify notification delivered.
      const data = await devAPI(backendBase, 'GET', '/dev/informer/notifications');
      assert(data.count > 0, `expected notifications after first publish, got count=${data.count}`);

      const msg = data.notifications[0];
      const text = msg?.text ?? msg;
      assert(typeof text === 'string' && text.length > 0, `notification text empty: ${JSON.stringify(msg)}`);
      assert(text.toLowerCase().includes('tbilisi'), `notification lacks "tbilisi": ${text}`);
      assert(
        text.includes('/v2/listing/' + tbilisiListing.listingId),
        `notification missing listing URL /v2/listing/${tbilisiListing.listingId}: ${text}`
      );

      await page.screenshot({ path: join(SCREENSHOTS_DIR, `inf_r${runNumber}_s4_notified.png`) });
    });

    // ── Step 5: Real reactivation → 0 notifications ──────────────────────────
    // Proves: Reactivate does NOT create an informer outbox event.
    await step('5: Real reactivation → 0 notifications (no outbox event on reactivate)', async () => {
      assert(tbilisiListing, 'tbilisiListing not set from step 4');

      // Drain stale notifications.
      await devAPI(backendBase, 'GET', '/dev/informer/notifications');

      // Expire listing (dev endpoint — allowed for lifecycle control).
      await devAPI(backendBase, 'POST', '/dev/listing/expire', {
        listing_id: tbilisiListing.listingId,
      });

      // Create new Telegram binding for reactivation (dev endpoint — Telegram capture).
      await devAPI(backendBase, 'POST', '/dev/telegram/connect', {
        management_code: tbilisiListing.managementCode,
        wallet_address: INFORMER_WALLET,
      });

      // Reactivate via real domain endpoint.
      const reactivateRes = await devAPI(backendBase, 'POST', '/v2/client/listings/reactivate', {
        management_code: tbilisiListing.managementCode,
        wallet_address: INFORMER_WALLET,
      });
      assert(reactivateRes.id === tbilisiListing.listingId, `unexpected listing id in reactivate response: ${reactivateRes.id}`);

      // Run worker — should find no pending outbox entries (reactivation creates none).
      await devAPI(backendBase, 'POST', '/dev/informer/run-worker');

      const data = await devAPI(backendBase, 'GET', '/dev/informer/notifications');
      assert(
        data.count === 0,
        `reactivation must NOT create informer notification, got count=${data.count}`
      );
    });

    // ── Step 6: City change (tbilisi → batumi) ───────────────────────────────
    await step('6: City change tbilisi → batumi replaces subscription', async () => {
      // Set up access response capture BEFORE navigation.
      const accessPromise2 = page.waitForResponse(
        r => r.url().includes('/v2/informer/access') && r.request().method() === 'POST',
        { timeout: 20000 }
      );

      // Navigate to fresh informer page.
      await page.goto(`${frontendBase}/v2/informer`);
      await page.waitForLoadState('domcontentloaded');

      await page.fill('input[type="text"]', INFORMER_WALLET);
      await page.waitForTimeout(300);
      await page.selectOption('select', 'batumi');

      const btn2 = page.locator('button.btn-primary:not(:disabled)').first();
      await btn2.waitFor({ timeout: 8000 });
      await btn2.click();

      // Capture raw_token for batumi.
      const accessResp2 = await accessPromise2;
      assert(accessResp2.status() === 200, `access for batumi returned ${accessResp2.status()}`);
      const accessData2 = await accessResp2.json();
      rawToken2 = accessData2.raw_token;
      assert(rawToken2 && rawToken2.length > 0, 'batumi raw_token empty');
      assert(rawToken2 !== rawToken, 'batumi token should differ from tbilisi token');

      // Wait for waiting state.
      await page.waitForSelector('.status-badge.waiting', { timeout: 12000 });

      // Watch for the status poll to return "claimed" for rawToken2.
      const watcher2 = watchForClaimedStatus(page);

      // Simulate /start for batumi token (same chat_id → replaces tbilisi subscription).
      await devAPI(backendBase, 'POST', '/dev/informer/simulate-start', { raw_token: rawToken2 });

      // Wait for browser status poll to detect "claimed".
      await watcher2.waitClaimed(18000);
      watcher2.stop();

      // DOM should show connected.
      await page.waitForSelector('.done-icon', { timeout: 8000 });

      await page.screenshot({ path: join(SCREENSHOTS_DIR, `inf_r${runNumber}_s6_batumi_connected.png`) });
    });

    // ── Step 7: Post city-change: tbilisi→0 notifs, batumi→1 notif ──────────
    // Uses real Client publish for both cities to prove domain-event routing.
    await step('7: Post city-change: real tbilisi publish→0 notifs, real batumi publish→1 notif', async () => {
      // Drain stale.
      await devAPI(backendBase, 'GET', '/dev/informer/notifications');

      // tbilisi publish — subscriber is now on batumi, should NOT deliver.
      await fullClientPublish(backendBase, INFORMER_WALLET, 'tbilisi');
      await devAPI(backendBase, 'POST', '/dev/informer/run-worker');
      const tbilisiData = await devAPI(backendBase, 'GET', '/dev/informer/notifications');
      assert(
        tbilisiData.count === 0,
        `tbilisi notification delivered after city change to batumi (count=${tbilisiData.count})`
      );

      // batumi publish — should deliver.
      await fullClientPublish(backendBase, INFORMER_WALLET, 'batumi');
      await devAPI(backendBase, 'POST', '/dev/informer/run-worker');
      const batumiData = await devAPI(backendBase, 'GET', '/dev/informer/notifications');
      assert(batumiData.count > 0, 'batumi notification not delivered after real publish');
      const msg = batumiData.notifications[0];
      const text = msg?.text ?? msg;
      assert(typeof text === 'string', `batumi notification text not a string: ${JSON.stringify(msg)}`);
      assert(text.toLowerCase().includes('batumi'), `notification lacks "batumi": ${text}`);
      assert(text.includes('/v2/listing/'), `notification missing listing URL: ${text}`);
    });

    // ── Step 8: /stop removes subscription → no notifications ─────────────────
    await step('8: /stop removes subscription — real batumi publish delivers 0 notifications', async () => {
      // Send /stop via direct webhook call (allowed — Telegram simulation).
      const status = await telegramStop(backendBase);
      assert(status === 200, `/stop webhook returned ${status}`);

      // Drain stale.
      await devAPI(backendBase, 'GET', '/dev/informer/notifications');

      // batumi publish after /stop — 0 notifications expected.
      await fullClientPublish(backendBase, INFORMER_WALLET, 'batumi');
      await devAPI(backendBase, 'POST', '/dev/informer/run-worker');
      const data = await devAPI(backendBase, 'GET', '/dev/informer/notifications');
      assert(data.count === 0, `notification delivered after /stop (count=${data.count})`);
    });

  } finally {
    if (page) await page.screenshot({ path: join(SCREENSHOTS_DIR, `inf_r${runNumber}_final.png`) }).catch(() => {});
    await context.close();
    await browser.close();
    await teardown(env);
  }

  console.log(`\n  Run ${runNumber}: ${passed} passed, ${failed} failed`);
  if (failures.length > 0) {
    console.error('  FAILURES:');
    for (const f of failures) console.error(`    • ${f.name}: ${f.error}`);
  }
  return { passed, failed, failures };
}

// ── Main ───────────────────────────────────────────────────────────────────────

async function main() {
  console.log('\nV2 Informer Browser E2E\n');

  let totalPassed = 0;
  let totalFailed = 0;
  const allFailures = [];

  for (let i = 1; i <= 2; i++) {
    const { passed, failed, failures } = await runOnce(i);
    totalPassed += passed;
    totalFailed += failed;
    allFailures.push(...failures.map(f => ({ run: i, ...f })));
  }

  console.log(`\n${'─'.repeat(60)}`);
  console.log(`TOTAL: ${totalPassed} passed, ${totalFailed} failed`);
  if (allFailures.length > 0) {
    console.error('\nFAILED:');
    for (const f of allFailures) console.error(`  Run ${f.run} • ${f.name}: ${f.error}`);
    process.exit(1);
  }
  console.log('ALL PASSED');
}

main().catch(err => {
  console.error('Fatal:', err);
  process.exit(1);
});
