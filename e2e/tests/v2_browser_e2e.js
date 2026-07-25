#!/usr/bin/env node
/**
 * V2 Browser E2E — Playwright with real UI interactions
 *
 * Starts V2 backend + Vite frontend on dynamic ports, runs 10 browser steps,
 * takes screenshots, verifies teardown. Two sequential runs.
 *
 * Dev API calls (devAPI helper) are ONLY used to simulate external events
 * that cannot happen in a test without a real blockchain/Telegram:
 *   - POST /dev/payment/confirm           (simulate blockchain confirmation)
 *   - POST /dev/telegram/simulate-start   (simulate Telegram /start via real webhook)
 *   - POST /dev/helper/payment/confirm
 *   - POST /dev/balance/set
 *   - POST /dev/listing/expire
 *   - POST /dev/telegram/review-callback  (routes through real transport webhook)
 *   - GET  /dev/helper/reputation         (helper aggregate counts)
 * All user actions (fill, click, navigate, read DOM) go through the browser.
 */

import { chromium } from 'playwright';
import { spawn, execSync } from 'child_process';
import { mkdtempSync, mkdirSync, existsSync, statSync } from 'fs';
import { tmpdir } from 'os';
import { join, resolve } from 'path';
import net from 'net';

const ROOT = resolve(new URL('../../', import.meta.url).pathname);
const SCREENSHOTS_DIR = join(ROOT, 'e2e', 'screenshots');
if (!existsSync(SCREENSHOTS_DIR)) mkdirSync(SCREENSHOTS_DIR, { recursive: true });

// Pre-build binary once
const DEV_BINARY = join(tmpdir(), 'naroom-v2-browser-e2e');
console.log('  Building V2 dev binary...');
execSync(`go build -o ${DEV_BINARY} ./cmd/naroom-v2-dev/`, {
  cwd: ROOT,
  stdio: ['ignore', 'ignore', 'inherit'],
  timeout: 120000,
});
console.log(`  ✓ Binary: ${DEV_BINARY}`);

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
  throw new Error(`Port ${port} still open after ${timeout}ms — teardown FAILED (process leak)`);
}

// Dev-only API (simulates external events: blockchain/Telegram)
async function devAPI(base, method, path, body) {
  const res = await fetch(`${base}${path}`, {
    method,
    headers: { 'Content-Type': 'application/json' },
    ...(body ? { body: JSON.stringify(body) } : {}),
  });
  const text = await res.text();
  let json = null;
  try { json = JSON.parse(text); } catch {}
  if (!res.ok) throw new Error(`devAPI ${method} ${path} → ${res.status}: ${text}`);
  return json;
}

function assert(cond, msg) {
  if (!cond) throw new Error(msg);
}

// Valid mainnet BTC bech32 P2WPKH (checksum-valid per btcutil)
// CLIENT and HELPER use different wallets to avoid balance/state collisions.
const CLIENT_WALLET = 'bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq';
const HELPER_WALLET = 'bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4';

// ── Process management ─────────────────────────────────────────────────────────

async function startTestEnv() {
  const backendPort = await findFreePort();
  const frontendPort = await findFreePort();
  const tmpDb = join(mkdtempSync(join(tmpdir(), 'v2-browser-e2e-')), 'naroom-v2.db');

  // Backend
  const backend = spawn(DEV_BINARY, [], {
    cwd: ROOT,
    env: { ...process.env, DEV_PORT: String(backendPort), DEV_DB_PATH: tmpDb },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  backend._lines = [];
  backend.stdout.on('data', d => backend._lines.push(...d.toString().split('\n')));
  backend.stderr.on('data', d => backend._lines.push(...d.toString().split('\n')));
  await waitForPort(backendPort, { label: 'backend', timeout: 15000 });

  // Frontend (Vite)
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
  frontend.on('exit', (code, sig) => {
    if (code !== null && code !== 0)
      console.warn(`  [vite] exited code=${code} sig=${sig}`);
  });
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
  // Fatal: both ports must close
  await assertPortClosed(env.backendPort, { timeout: 5000 });
  await assertPortClosed(env.frontendPort, { timeout: 5000 });
  console.log(`  ✓ ports ${env.backendPort} and ${env.frontendPort} released`);
}

// ── Single E2E run ─────────────────────────────────────────────────────────────

async function runOnce(runNumber) {
  console.log(`\n  ── Browser Run ${runNumber} ─────────────────────────────────`);
  const env = await startTestEnv();
  const { backendBase, frontendBase } = env;

  // Browser context with clipboard permissions so copy-btn assertions work.
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({ permissions: ['clipboard-read', 'clipboard-write'] });

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

  // Shared state across steps — persists through all 10 steps within a run.
  let managementCode = '';
  let flowId = '';
  let listingId = '';
  let purchaseToken = '';
  let purchaseId = '';

  // clientPage is kept alive across steps 1-5 and 10.
  // helperPage is created in step 6 and kept alive through steps 6-9.
  let clientPage = null;
  let helperPage = null;

  try {

    // ── Step 1: wallet → code gate (BEFORE invoice) → invoice ────────────────
    await step('1: Wallet entry → code gate first → then invoice (correct order)', async () => {
      clientPage = await context.newPage();
      clientPage.on('popup', p => { p.close().catch(() => {}); });
      await clientPage.setViewportSize({ width: 1440, height: 900 });
      await clientPage.goto(`${frontendBase}/v2/new`);
      await clientPage.waitForLoadState('networkidle');

      // Fill wallet
      await clientPage.fill('input[type="text"]', CLIENT_WALLET);
      await clientPage.waitForTimeout(400);

      // Click the primary button ("Continue" / "$5")
      const btn = clientPage.locator('button.btn-primary:not(:disabled)').first();
      await btn.waitFor({ timeout: 5000 });
      await btn.click();

      // CODE GATE must appear BEFORE invoice
      await clientPage.waitForSelector('.code-box', { timeout: 10000 });
      const invoiceNow = await clientPage.locator('.invoice-box').count();
      assert(invoiceNow === 0, 'invoice appeared before code gate — wrong step order');

      managementCode = ((await clientPage.locator('code.code-text').textContent()) || '').trim();
      assert(managementCode.length > 0, 'management_code empty in code gate');

      // Acknowledge
      await clientPage.check('input[type="checkbox"]');
      await clientPage.waitForTimeout(200);

      // Continue → invoice
      await clientPage.click('button.btn-primary:not(:disabled)');
      await clientPage.waitForSelector('.invoice-box', { timeout: 10000 });

      // No external QR
      const qrExt = await clientPage.locator('img[src*="qrserver"]').count();
      assert(qrExt === 0, 'qrserver.com external QR still present — must use local V2QR');

      // Local V2QR must render
      const localQR = await clientPage.locator('.v2qr svg, .v2qr').count();
      assert(localQR > 0, 'local V2QR component not rendered on invoice step');

      // Exact 8-decimal invoice amount
      const amtText = await clientPage.locator('.inv-val').first().textContent();
      assert(amtText && amtText.includes('.'), 'invoice amount missing decimal');
      const match = (amtText || '').match(/(\d+\.\d+)/);
      if (match) assert(match[1].split('.')[1].length === 8, `amount not 8 decimals: ${match[1]}`);

      // Clipboard copy button: value must match 8-decimal BTC format
      await clientPage.locator('button.copy-btn').first().click();
      const clipText = await clientPage.evaluate(() => navigator.clipboard.readText());
      assert(/^\d+\.\d{8}$/.test(clipText), `clipboard value not 8-decimal BTC: "${clipText}"`);

      // Read flowId from sessionStorage
      flowId = await clientPage.evaluate(() => {
        try { return JSON.parse(sessionStorage.getItem('v2_client_state') || '{}').flowId || ''; }
        catch { return ''; }
      });
      assert(flowId.length > 0, 'flowId not saved to sessionStorage');

      await clientPage.screenshot({ path: join(SCREENSHOTS_DIR, `run${runNumber}_01_invoice.png`) });
    });

    // ── Step 2: Dev confirm → balance step → click Continue → Telegram step ───
    await step('2: Payment confirmed → balance → Telegram step', async () => {
      // External event: blockchain confirmation
      await devAPI(backendBase, 'POST', '/dev/payment/confirm', {
        flow_id: flowId,
        wallet_address: CLIENT_WALLET,
      });

      // Page is already at /v2/new with sessionStorage intact — poll picks up state change.
      // Wait up to 20s for page to poll and transition to balance or telegram step.
      await clientPage.waitForFunction(
        () =>
          document.querySelector('h2')?.textContent?.toLowerCase().includes('balance') ||
          document.querySelector('h2')?.textContent?.toLowerCase().includes('telegram'),
        null,
        { timeout: 20000 }
      );

      const h2 = ((await clientPage.locator('h2').first().textContent()) || '').toLowerCase();
      if (h2.includes('balance')) {
        // Balance step — click Continue to advance to Telegram step
        await clientPage.click('button.btn-primary:not(:disabled)');
      }

      // Wait for Telegram step UI: either a tg-btn or ok-badge must appear
      await clientPage.waitForSelector('button.tg-btn, .ok-badge', { timeout: 15000 });

      await clientPage.screenshot({ path: join(SCREENSHOTS_DIR, `run${runNumber}_02_balance.png`) });
    });

    // ── Step 3: Browser clicks Connect Telegram → dev simulate-start → form ──
    await step('3: Click Connect Telegram → simulate-start → form step auto-advances', async () => {
      // Must be on telegram step with the tg-btn (initial state, not yet link_pending)
      await clientPage.waitForSelector('button.tg-btn', { timeout: 10000 });

      // Click the Connect Telegram button — starts a pending attempt in the backend
      // and triggers the 3-second poll. The page will show link_pending state
      // with an <a class="tg-btn"> anchor after the first poll cycle.
      await clientPage.locator('button.tg-btn').first().click();

      // Wait for link_pending anchor: <a class="tg-btn" href*="t.me">
      await clientPage.waitForSelector('a.tg-btn[href*="t.me"]', { timeout: 15000 });

      // Extract raw token from the bot URL ?start= param
      const href = await clientPage.locator('a.tg-btn[href*="t.me"]').first().getAttribute('href');
      assert(href, 'tg-btn anchor has no href');
      const tokenMatch = href.match(/[?&]start=([^&]+)/);
      assert(tokenMatch, `no ?start= param in bot_url: ${href}`);
      const rawToken = tokenMatch[1];

      // External event: simulate Telegram sending /start <rawToken> via real webhook
      await devAPI(backendBase, 'POST', '/dev/telegram/simulate-start', { raw_token: rawToken });

      // Poll detects binding=ready → step auto-advances to form.
      // Wait for chip-group (form step selector).
      await clientPage.waitForSelector('.chip-group', { timeout: 20000 });

      await clientPage.screenshot({ path: join(SCREENSHOTS_DIR, `run${runNumber}_03_telegram.png`) });
    });

    // ── Step 4: Fill form → publish → done screen ─────────────────────────────
    await step('4: Fill listing form → publish → done screen with listing link', async () => {
      // Already on form step (chip-group visible from step 3)
      await clientPage.waitForSelector('.chip-group', { timeout: 10000 });

      // Dependency type — pick first chip
      await clientPage.locator('.chip-group').first().locator('.chip').first().click();
      // Help type — second chip-group, first chip
      await clientPage.locator('.chip-group').nth(1).locator('.chip').first().click();
      // Urgency — third chip-group
      await clientPage.locator('.chip-group').nth(2).locator('.chip').first().click();
      // Language — fourth chip-group
      await clientPage.locator('.chip-group').nth(3).locator('.chip').first().click();
      // Contact
      await clientPage.locator('input.contact-val').fill('@testv2e2erun' + runNumber);
      await clientPage.waitForTimeout(300);

      // Publish
      await clientPage.click('button.btn-primary:not(:disabled)');
      await clientPage.waitForSelector('.done-icon, .done', { timeout: 15000 });
      // Wait for the listing link to appear (rendered conditionally after listingId is set)
      await clientPage.waitForSelector('a[href*="/v2/listing/"]', { timeout: 5000 }).catch(() => {});

      // Get listing ID from the done screen link
      listingId = await clientPage.evaluate(() => {
        const a = document.querySelector('a[href*="/v2/listing/"]');
        if (!a) return '';
        const m = a.href.match(/\/v2\/listing\/([^/?#]+)/);
        return m ? m[1] : '';
      });
      assert(listingId.length > 0, 'listing ID not found on done screen');

      await clientPage.screenshot({ path: join(SCREENSHOTS_DIR, `run${runNumber}_04_done.png`) });
    });

    // ── Step 5: Board shows listing; no privacy leaks; no overflow ────────────
    await step('5: Board shows listing; privacy fields absent; no horizontal overflow at 390px', async () => {
      // Desktop check — reuse clientPage at 1440px
      await clientPage.setViewportSize({ width: 1440, height: 900 });
      await clientPage.goto(`${frontendBase}/v2/board/tbilisi`);
      await clientPage.waitForLoadState('networkidle');
      await clientPage.waitForSelector('.card.listing', { timeout: 10000 });

      // Board must not expose contact or management code
      const boardHTML = await clientPage.locator('.card.listing').first().innerHTML();
      assert(!boardHTML.includes('@testv2e2erun'), 'contact value visible on board — security violation');
      assert(!boardHTML.includes('management_code'), 'management_code visible on board — security violation');

      await clientPage.screenshot({ path: join(SCREENSHOTS_DIR, `run${runNumber}_05_board_desktop.png`) });

      // Mobile overflow check
      await clientPage.setViewportSize({ width: 390, height: 844 });
      await clientPage.goto(`${frontendBase}/v2/board/tbilisi`);
      await clientPage.waitForLoadState('networkidle');
      await clientPage.waitForTimeout(600);

      const ov = await clientPage.evaluate(() => ({
        bodyW: document.body.scrollWidth,
        clientW: document.body.clientWidth,
        htmlW: document.documentElement.scrollWidth,
        htmlCW: document.documentElement.clientWidth,
      }));
      // City tabs scroll inside their own container — page body must NOT overflow
      assert(ov.bodyW <= ov.clientW,
        `body overflow at 390px: scrollWidth=${ov.bodyW} > clientWidth=${ov.clientW}`);
      assert(ov.htmlW <= ov.htmlCW,
        `html overflow at 390px: scrollWidth=${ov.htmlW} > clientWidth=${ov.htmlCW}`);

      await clientPage.screenshot({ path: join(SCREENSHOTS_DIR, `run${runNumber}_05_board_mobile.png`) });
    });

    // ── Step 6: Helper purchase → no ?pt= → invoice → clipboard ──────────────
    await step('6: Listing detail → helper wallet → purchase → URL has no ?pt= → invoice + clipboard', async () => {
      // Pre-set balance above $1000 helper floor
      await devAPI(backendBase, 'POST', '/dev/balance/set', {
        wallet_address: HELPER_WALLET,
        balance_usd: 2000.0,
      });

      // Create helperPage from same context (clipboard perms inherited)
      helperPage = await context.newPage();
      helperPage.on('popup', p => { p.close().catch(() => {}); });
      await helperPage.setViewportSize({ width: 390, height: 844 });
      await helperPage.goto(`${frontendBase}/v2/listing/${listingId}`);
      await helperPage.waitForLoadState('networkidle');

      // Informational notice must be visible before wallet input
      const noticeCount = await helperPage.locator('.notice-box, .notice-title').count();
      assert(noticeCount > 0, 'informational notice not shown on listing page');

      // Fill helper wallet
      await helperPage.fill('input[type="text"]', HELPER_WALLET);
      await helperPage.waitForTimeout(500);

      // Click "Get contact" / helper purchase button
      await helperPage.click('button.btn-primary:not(:disabled)');
      await helperPage.waitForTimeout(2000);

      // URL must NOT expose ?pt=
      const url = helperPage.url();
      assert(!url.includes('?pt=') && !url.includes('&pt=') && !url.includes('%3Fpt%3D'),
        `purchase token exposed in URL: ${url}`);
      assert(url.includes('/v2/helper/purchase'),
        `expected /v2/helper/purchase, got: ${url}`);

      // Invoice must be visible
      await helperPage.waitForSelector('.invoice-box', { timeout: 15000 });

      // No external QR
      const extQR = await helperPage.locator('img[src*="qrserver"]').count();
      assert(extQR === 0, 'qrserver.com QR image present on helper invoice page');

      // Clipboard copy button: value must match 8-decimal BTC format
      await helperPage.locator('button.copy-btn').first().click();
      const clipText = await helperPage.evaluate(() => navigator.clipboard.readText());
      assert(/^\d+\.\d{8}$/.test(clipText), `clipboard value not 8-decimal BTC: "${clipText}"`);

      // Read purchaseToken and purchaseId from sessionStorage
      purchaseToken = await helperPage.evaluate(() => {
        try { return sessionStorage.getItem('v2_active_purchase_token') || ''; } catch { return ''; }
      });
      assert(purchaseToken.length > 0, 'purchase_token not found in sessionStorage');

      const saved = await helperPage.evaluate((pt) => {
        try { return JSON.parse(sessionStorage.getItem(`v2_purchase_${pt}`) || 'null'); } catch { return null; }
      }, purchaseToken);
      assert(saved && saved.purchaseId, 'purchaseId not in sessionStorage');
      purchaseId = saved.purchaseId;

      await helperPage.screenshot({ path: join(SCREENSHOTS_DIR, `run${runNumber}_06_helper_invoice.png`) });
    });

    // ── Step 7: Dev confirm helper payment → SAME helperPage.reload() → contact
    await step('7: Helper payment confirmed → reload same tab → contact revealed → open-btn present', async () => {
      // External event: blockchain confirmation
      await devAPI(backendBase, 'POST', '/dev/helper/payment/confirm', {
        purchase_id: purchaseId,
        wallet_address: HELPER_WALLET,
      });

      // Reload the same helperPage — sessionStorage is preserved, no seeding needed
      await helperPage.reload();
      await helperPage.waitForLoadState('networkidle');

      await helperPage.waitForSelector('.cv-text', { timeout: 20000 });
      const contact = (await helperPage.locator('.cv-text').textContent() || '').trim();
      assert(contact.length > 0, 'contact value empty after reveal');

      // Open button must be present (safe open action)
      const openBtn = await helperPage.locator('button.open-btn, button:has-text("Open →"), button:has-text("Open")').count();
      assert(openBtn > 0, 'open-btn not found on contact screen');

      await helperPage.screenshot({ path: join(SCREENSHOTS_DIR, `run${runNumber}_07_contact.png`) });
    });

    // ── Step 8: Reload SAME helperPage → contact from sessionStorage ──────────
    await step('8: Reload same helperPage (no ?pt=) → contact restored from sessionStorage', async () => {
      await helperPage.reload();
      await helperPage.waitForLoadState('networkidle');

      // URL must have no ?pt= query param
      const url = helperPage.url();
      assert(!url.includes('?pt=') && !url.includes('&pt='),
        `URL has ?pt= after reload: ${url}`);

      // Contact must still show from sessionStorage restore
      await helperPage.waitForSelector('.cv-text', { timeout: 15000 });
      const contact = (await helperPage.locator('.cv-text').textContent() || '').trim();
      assert(contact.length > 0, 'contact not restored after reload — sessionStorage restore failed');
    });

    // ── Step 9: Helper review → webhook callback → helper aggregate ───────────
    await step('9: Helper review → client callback via webhook → aggregate +1 exactly once; replay idempotent', async () => {
      // helperPage is still on the contact page — review buttons must be present. MANDATORY.
      await helperPage.locator('button.review-btn, button.review-btn.positive').first().waitFor({ timeout: 10000 });

      // Click first review button (positive)
      await helperPage.locator('button.review-btn, button.review-btn.positive').first().click();

      // review-done is MANDATORY — if it fails, the test fails (no catch)
      await helperPage.waitForSelector('.review-done', { timeout: 10000 });

      // Capture client aggregate BEFORE Telegram callback (helper UI review may have incremented it)
      const boardBefore = await fetch(`${frontendBase}/api/v2/board/tbilisi`);
      const boardDataBefore = await boardBefore.json();
      const listingBefore = boardDataBefore.find(l => l.id === listingId);
      assert(listingBefore, 'listing not on board before review callback');
      const clientAggBefore = listingBefore.client_reputation.positive_count;

      // External event: simulate Client clicking Telegram inline button via real webhook
      const notifs = await devAPI(backendBase, 'GET', '/dev/notifications');
      const n = (notifs.notifications || [])[0];
      assert(n && n.pos_data, 'no review notification with pos_data found after helper review');

      await devAPI(backendBase, 'POST', '/dev/telegram/review-callback', { callback_data: n.pos_data });

      // Verify helper aggregate +1 via dev endpoint (not board — board shows client aggregate)
      const rep = await devAPI(backendBase, 'GET', `/dev/helper/reputation?purchase_id=${purchaseId}`);
      assert(rep.positive_count >= 1, `helper positive_count expected ≥1, got ${rep.positive_count}`);
      const repCount1 = rep.positive_count;

      // Board check: client_reputation.positive_count must be UNCHANGED by CLIENT review callback
      // (helper's UI review increments client aggregate; Telegram CLIENT callback → helper aggregate)
      const boardAfter = await fetch(`${frontendBase}/api/v2/board/tbilisi`);
      const boardDataAfter = await boardAfter.json();
      const listingAfter = boardDataAfter.find(l => l.id === listingId);
      assert(listingAfter, 'listing not on board after review callback');
      assert(
        listingAfter.client_reputation.positive_count === clientAggBefore,
        `client aggregate changed by CLIENT callback: was ${clientAggBefore}, now ${listingAfter.client_reputation.positive_count}`
      );

      // Replay: same callback_data must NOT double-increment
      try {
        await devAPI(backendBase, 'POST', '/dev/telegram/review-callback', { callback_data: n.pos_data });
      } catch { /* expected: 422 already consumed */ }

      const rep2 = await devAPI(backendBase, 'GET', `/dev/helper/reputation?purchase_id=${purchaseId}`);
      assert(rep2.positive_count === repCount1,
        `double-increment after replay: was ${repCount1}, now ${rep2.positive_count}`);

      // Opposite: neg_data must also not change positive count
      if (n.neg_data) {
        try {
          await devAPI(backendBase, 'POST', '/dev/telegram/review-callback', { callback_data: n.neg_data });
        } catch { /* expected: already consumed */ }
        const rep3 = await devAPI(backendBase, 'GET', `/dev/helper/reputation?purchase_id=${purchaseId}`);
        assert(rep3.positive_count === repCount1,
          `positive_count changed after neg replay: was ${repCount1}, now ${rep3.positive_count}`);
      }

      await helperPage.screenshot({ path: join(SCREENSHOTS_DIR, `run${runNumber}_09_review.png`) });
    });

    // ── Step 10: Expire → navigate /v2/new → Connect Telegram → reactivate ───
    await step('10: Daily expire → navigate /v2/new → fresh Connect Telegram → auto-reactivate', async () => {
      // External event: force listing to 'hidden' AND delete binding (binding TTL simulated)
      await devAPI(backendBase, 'POST', '/dev/listing/expire', { listing_id: listingId });

      // Intercept reactivation responses to verify the call is made
      const reactivationResponses = [];
      clientPage.on('response', async (resp) => {
        if (resp.url().includes('/listings/reactivate')) {
          try { reactivationResponses.push({ status: resp.status(), body: (await resp.text()).slice(0, 200) }); } catch {}
        }
      });

      // Navigate clientPage to /v2/new — same tab, sessionStorage has managementCode/flowId/walletAddress
      // onMount will restore from v2_client_state and detect phase=hidden → isReactivating=true.
      await clientPage.setViewportSize({ width: 1440, height: 900 });
      await clientPage.goto(`${frontendBase}/v2/new`);

      // Wait up to 15s for the UI to restore and show: tg-btn (Telegram step), ok-badge, or done-icon.
      // NOTE: must NOT use '.done' here — the progress bar renders <div class="prog-step done"> for
      // completed steps even when step='telegram', so '.done' count > 0 is NOT the done screen.
      await clientPage.waitForSelector('button.tg-btn, .ok-badge, .done-icon', { timeout: 15000 });

      // Use only '.done-icon' (not '.done') — the progress bar adds class 'done' to completed step dots.
      const isDone = await clientPage.locator('.done-icon').count();
      if (isDone === 0) {
        // Telegram step is shown (binding was deleted by expire, need fresh connect)
        await clientPage.waitForSelector('button.tg-btn', { timeout: 5000 });
        await clientPage.locator('button.tg-btn').first().click();

        // Wait for link_pending anchor with real bot URL
        await clientPage.waitForSelector('a.tg-btn[href*="t.me"]', { timeout: 15000 });
        const href = await clientPage.locator('a.tg-btn[href*="t.me"]').first().getAttribute('href');
        assert(href, 'tg-btn anchor has no href on reactivation');
        const tokenMatch = href.match(/[?&]start=([^&]+)/);
        assert(tokenMatch, `no ?start= param in bot_url on reactivation: ${href}`);
        const rawToken = tokenMatch[1];

        // External event: simulate Telegram /start via real webhook
        await devAPI(backendBase, 'POST', '/dev/telegram/simulate-start', { raw_token: rawToken });

        // Poll detects binding=ready → isReactivating=true → reactivateListing() auto-called → done.
        // Use only '.done-icon' (not '.done') — progress bar adds class 'done' to completed steps.
        await clientPage.waitForSelector('.done-icon', { timeout: 20000 });
      }

      await clientPage.waitForTimeout(300); // allow response events to flush

      // Listing must be back on board (no new $5 invoice required)
      const boardR = await fetch(`${frontendBase}/api/v2/board/tbilisi`);
      const boardText = await boardR.text();
      let board;
      try { board = JSON.parse(boardText); } catch { board = []; }
      const reactivated = Array.isArray(board) && board.find(l => l.id === listingId);
      assert(reactivated, `listing NOT back on board after reactivation — five-day window consumed incorrectly\n  listingId=${listingId}\n  boardStatus=${boardR.status}\n  board=${boardText.slice(0, 500)}`);

      await clientPage.screenshot({ path: join(SCREENSHOTS_DIR, `run${runNumber}_10_reactivated.png`) });
    });

  } finally {
    // Close both persistent pages
    if (clientPage) { try { await clientPage.close(); } catch {} }
    if (helperPage) { try { await helperPage.close(); } catch {} }
    await context.close();
    await browser.close();
    await teardown(env);
  }

  // Verify all screenshot files exist and have non-zero size
  const screenshots = [
    '01_invoice', '04_done', '05_board_desktop', '05_board_mobile',
    '06_helper_invoice', '07_contact', '09_review', '10_reactivated',
  ].map(s => join(SCREENSHOTS_DIR, `run${runNumber}_${s}.png`));
  for (const f of screenshots) {
    const st = statSync(f);
    assert(st.size > 0, `screenshot empty: ${f}`);
    console.log(`    ✓ screenshot: ${f}`);
  }

  console.log(`\n  Run ${runNumber}: ${passed}/${passed + failed} steps passed`);
  if (failures.length > 0) {
    for (const f of failures) console.error(`    FAILED: ${f.name} — ${f.error}`);
  }

  return { passed, failed, failures };
}

// ── Entry point ────────────────────────────────────────────────────────────────

console.log('\n╔══════════════════════════════════════════════════════╗');
console.log('║  NA Room V2 — Browser E2E Test (Playwright UI)      ║');
console.log('╚══════════════════════════════════════════════════════╝');

const results = [];
for (let run = 1; run <= 2; run++) {
  try {
    results.push(await runOnce(run));
  } catch (e) {
    console.error(`  FATAL run ${run}: ${e.stack || e.message}`);
    results.push({ passed: 0, failed: 10, failures: [{ name: 'fatal', error: e.message }] });
  }
  if (run < 2) {
    console.log('\n  Waiting 2s between runs...');
    await sleep(2000);
  }
}

console.log('\n══════════════════════════════════════════════════════');
const totalPassed = results.reduce((s, r) => s + r.passed, 0);
const totalFailed = results.reduce((s, r) => s + r.failed, 0);
console.log(`  TOTAL: ${totalPassed}/${totalPassed + totalFailed} steps across ${results.length} runs`);

if (results.every(r => r.failed === 0)) {
  console.log('  RESULT: ALL PASSED ✓');
} else {
  console.error('  RESULT: FAILED ✗');
  process.exit(1);
}
