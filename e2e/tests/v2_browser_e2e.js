#!/usr/bin/env node
/**
 * V2 Browser E2E — Playwright with real UI interactions
 *
 * Starts V2 backend + Vite frontend on dynamic ports, runs the full browser
 * journey (create/publish, owner mode, private-window public mode, self-purchase
 * guard, cross-device handoff threat matrix, helper purchase, review lifecycle,
 * hidden-listing reactivation, restore-page matrix), takes screenshots, verifies
 * teardown. Two sequential runs.
 *
 * Context/persona model:
 *   - `context` (with clipboard perms) is the CLIENT's own browser throughout
 *     the run — clientPage lives here from step 1 through the end.
 *   - `helperContext` is a SEPARATE browser context representing the Helper's
 *     own device — helperPage lives here. This matters once owner mode exists:
 *     a page sharing the Client's context/localStorage would incorrectly see
 *     owner mode when visiting the Client's own listing.
 *   - Any step that needs to simulate "a different person/device" (balance
 *     outage check, self-purchase attempt, handoff redeem, restore-page entry)
 *     uses a fresh `browser.newContext()` — real incognito-style isolation,
 *     no shared storage with `context` or `helperContext`.
 *
 * Dev API calls (devAPI helper) are ONLY used to simulate external events
 * that cannot happen in a test without a real blockchain/Telegram:
 *   - POST /dev/payment/confirm           (simulate blockchain confirmation)
 *   - POST /dev/telegram/simulate-start   (simulate Telegram /start via real webhook)
 *   - POST /dev/helper/payment/confirm
 *   - POST /dev/helper/invoice/expire
 *   - POST /dev/balance/set
 *   - POST /dev/listing/expire
 *   - POST /dev/telegram/review-callback  (routes through real transport webhook)
 *   - GET  /dev/helper/reputation         (helper aggregate counts)
 *   - GET  /dev/db/counts                 (domain-effect row counts — E2E-only)
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

  const browser = await chromium.launch({ headless: true });
  // Client persona: clipboard perms needed for invoice/code copy-button assertions.
  const context = await browser.newContext({ permissions: ['clipboard-read', 'clipboard-write'] });

  let passed = 0;
  let failed = 0;
  const failures = [];
  // Tracks pages/contexts opened mid-run so a failure doesn't leak them past teardown.
  const scratchContexts = [];

  async function withIncognito(fn) {
    const ctx = await browser.newContext({ permissions: ['clipboard-read', 'clipboard-write'] });
    scratchContexts.push(ctx);
    const page = await ctx.newPage();
    page.on('popup', p => { p.close().catch(() => {}); });
    try {
      return await fn(page, ctx);
    } finally {
      await ctx.close().catch(() => {});
      const idx = scratchContexts.indexOf(ctx);
      if (idx >= 0) scratchContexts.splice(idx, 1);
    }
  }

  async function step(name, fn) {
    try {
      await fn();
      console.log(`    ✓ ${name}`);
      passed++;
    } catch (e) {
      console.error(`    ✗ ${name}: ${e.message}`);
      failed++;
      failures.push({ name, error: e.message });
      // Print last 20 backend log lines to aid debugging
      if (env && env.backend && env.backend._lines) {
        const lines = env.backend._lines.slice(-20).filter(l => l.trim());
        if (lines.length > 0) console.error('    [backend log]', lines.join('\n    [backend log] '));
      }
    }
  }

  // Shared state across steps — persists through the whole run.
  let managementCode = '';
  let flowId = '';
  let listingId = '';
  let purchaseToken = '';
  let purchaseId = '';

  // clientPage lives in `context` (Client's own browser) for the whole run.
  // helperPage lives in a SEPARATE `helperContext` (Helper's own device) from
  // step 6 onward — critical once owner mode exists (see file header).
  let clientPage = null;
  let helperContext = null;
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

      // V2QR renders its SVG asynchronously after the invoice shell appears.
      await clientPage.locator('.v2qr svg').waitFor({ state: 'visible', timeout: 10000 });

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
    await step('4: Fill listing form → publish → done screen with Manage-listing link', async () => {
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
      // Wait for the "Manage listing" link to appear (rendered conditionally after listingId is set)
      await clientPage.waitForSelector('a[href*="/v2/listing/"]', { timeout: 5000 }).catch(() => {});

      // Button must now say "Manage listing", not the old "View listing".
      const manageBtn = clientPage.locator('[data-testid="manage-listing-btn"]');
      assert(await manageBtn.count() > 0, 'Manage-listing button (data-testid=manage-listing-btn) not found on done screen');

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

    // ── Step 4b: Owner mode — Client's own browser sees "Your listing", no purchase form ─
    await step('4b: Owner mode — Client visiting own listing sees Your listing, no Helper form, Back to board', async () => {
      // Same shared `context` as clientPage: localStorage carries the owner
      // capability (v2_mgmt_<id>) saved by /v2/new on publish success.
      await clientPage.goto(`${frontendBase}/v2/listing/${listingId}`);
      await clientPage.waitForLoadState('networkidle');
      await clientPage.waitForSelector('[data-testid="owner-mode"]', { timeout: 10000 });

      // Helper wallet form / progress / purchase button must be entirely absent.
      const walletFormCount = await clientPage.locator('.field input[type="text"]').count();
      assert(walletFormCount === 0, 'owner mode: Helper wallet form must not render');
      const purchaseBtnCount = await clientPage.locator('button:has-text("Get contact")').count();
      assert(purchaseBtnCount === 0, 'owner mode: Get-contact purchase button must not render');
      const progressCount = await clientPage.locator('.progress-bar').count();
      assert(progressCount === 0, 'owner mode: Helper progress bar must not render');

      // State must be visible (published just now) with a remaining-time indicator.
      const stateText = ((await clientPage.locator('[data-testid="owner-state"]').textContent()) || '').toLowerCase();
      assert(stateText.length > 0, 'owner state text empty');

      // Back to board must always be present.
      assert(await clientPage.locator('[data-testid="owner-back-board"]').count() > 0,
        'owner mode: Back to board link missing');

      await clientPage.screenshot({ path: join(SCREENSHOTS_DIR, `run${runNumber}_04b_owner_mode.png`) });
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

    // ── Step 5b: Balance outage → error shown, Retry visible, no invoice created ─
    await step('5b: Balance outage → error shown, Retry button visible, no invoice created', async () => {
      await devAPI(backendBase, 'POST', '/dev/balance/outage', { enabled: true });

      // Fresh incognito context: a real Helper device, never shares the Client's
      // localStorage (so owner mode never fires here regardless of listing state).
      await withIncognito(async (tempPage) => {
        await tempPage.setViewportSize({ width: 390, height: 844 });
        await tempPage.goto(`${frontendBase}/v2/listing/${listingId}`);
        await tempPage.waitForLoadState('networkidle');
        await tempPage.waitForSelector('input[type="text"]', { timeout: 10000 });

        await tempPage.fill('input[type="text"]', HELPER_WALLET);
        await tempPage.waitForTimeout(300);

        // Click "Get contact" — balance outage causes 503
        await tempPage.click('button.btn-primary:not(:disabled)');
        await tempPage.waitForTimeout(1500);

        // Error message must mention balance/unavailable
        const errText = (await tempPage.locator('.err').textContent().catch(() => ''));
        assert(errText.toLowerCase().includes('unavailable') || errText.toLowerCase().includes('balance'),
          `expected balance unavailable error, got: "${errText}"`);

        // NO invoice should have been created
        const invoiceBoxCount = await tempPage.locator('.invoice-box').count();
        assert(invoiceBoxCount === 0, 'invoice-box appeared despite balance outage — invoice was wrongly created');

        // Retry button must be visible
        const retryCount = await tempPage.locator('button.btn-secondary').count();
        assert(retryCount > 0, 'Retry button not visible after balance outage error');

        // Main "Get contact" button must be disabled during outage state
        const disabledCount = await tempPage.locator('button.btn-primary[disabled]').count();
        assert(disabledCount > 0, 'main purchase button not disabled during balance outage');

        await tempPage.screenshot({ path: join(SCREENSHOTS_DIR, `run${runNumber}_5b_outage.png`) });
      });

      await devAPI(backendBase, 'POST', '/dev/balance/outage', { enabled: false });
    });

    // ── Step 5c: wallet_already_visible — second listing attempt rejected ──────
    await step('5c: wallet_already_visible — localized error, no invoice, 0 new client flows/invoices', async () => {
      const before = await devAPI(backendBase, 'GET', '/dev/db/counts');

      // CLIENT_WALLET already has a visible listing from step 4.
      // Attempting to create another payment intent for the same wallet must return 409
      // with code=wallet_already_visible, and the page must show a localized error message.
      await withIncognito(async (tempPage) => {
        await tempPage.setViewportSize({ width: 390, height: 844 });
        await tempPage.goto(`${frontendBase}/v2/new`);
        await tempPage.waitForLoadState('networkidle');
        await tempPage.waitForSelector('input[type="text"]', { timeout: 10000 });

        // Fill same wallet that already has a listing
        await tempPage.fill('input[type="text"]', CLIENT_WALLET);
        await tempPage.waitForTimeout(400);

        // Click primary button — should trigger wallet_already_visible 409
        await tempPage.click('button.btn-primary:not(:disabled)');

        // Might hit code gate first — if code-box appears, fill checkbox and continue
        const codeBoxVisible = await tempPage.locator('.code-box').isVisible().catch(() => false);
        if (codeBoxVisible) {
          await tempPage.check('input[type="checkbox"]');
          await tempPage.waitForTimeout(200);
          await tempPage.click('button.btn-primary:not(:disabled)');
        }

        // Wait for error — must appear (no invoice-box must follow)
        await tempPage.waitForSelector('.err', { timeout: 10000 });
        const errText = ((await tempPage.locator('.err').first().textContent()) || '').toLowerCase();
        assert(
          errText.includes('wallet') || errText.includes('already') || errText.includes('active') || errText.includes('listing'),
          `expected wallet_already_visible error, got: "${errText}"`
        );

        // Invoice must NOT have appeared
        const invoiceCount = await tempPage.locator('.invoice-box').count();
        assert(invoiceCount === 0, 'invoice-box appeared after wallet_already_visible — guard failed');

        await tempPage.screenshot({ path: join(SCREENSHOTS_DIR, `run${runNumber}_5c_wallet_already.png`) });
      });

      const after = await devAPI(backendBase, 'GET', '/dev/db/counts');
      assert(after.v2_client_flows === before.v2_client_flows,
        `wallet_already_visible must create 0 new client flows: before=${before.v2_client_flows}, after=${after.v2_client_flows}`);
      assert(after.v2_invoices === before.v2_invoices,
        `wallet_already_visible must create 0 new client invoices: before=${before.v2_invoices}, after=${after.v2_invoices}`);
    });

    // ── Step 5d: Self-purchase — private-window public mode + 0 external effects ─
    await step('5d: Private-window public mode + self-purchase guard — 0 new helper purchases/invoices', async () => {
      const before = await devAPI(backendBase, 'GET', '/dev/db/counts');

      await withIncognito(async (tempPage) => {
        await tempPage.setViewportSize({ width: 390, height: 844 });
        await tempPage.goto(`${frontendBase}/v2/listing/${listingId}`);
        await tempPage.waitForLoadState('networkidle');

        // Private window (no owner localStorage): must see the ORDINARY public
        // Helper view, never owner mode.
        assert(await tempPage.locator('[data-testid="owner-mode"]').count() === 0,
          'private window incorrectly shows owner mode — storage must not be an authorization basis');
        await tempPage.waitForSelector('input[type="text"]', { timeout: 10000 });

        // CLIENT_WALLET owns listingId. Trying to purchase it as a helper must fail —
        // guard runs before any balance/invoice provider call.
        await tempPage.fill('input[type="text"]', CLIENT_WALLET);
        await tempPage.waitForTimeout(300);
        await tempPage.click('button.btn-primary:not(:disabled)');
        await tempPage.waitForTimeout(2000);

        // Must show an error — no invoice
        const errText = ((await tempPage.locator('.err').first().textContent().catch(() => '')) || '').toLowerCase();
        assert(errText.length > 0, 'no error shown for self-purchase attempt');

        // Must NOT redirect to /v2/helper/purchase
        assert(
          !tempPage.url().includes('/v2/helper/purchase'),
          `redirected to purchase page on self-purchase attempt: ${tempPage.url()}`
        );

        await tempPage.screenshot({ path: join(SCREENSHOTS_DIR, `run${runNumber}_5d_self_purchase.png`) });
      });

      const after = await devAPI(backendBase, 'GET', '/dev/db/counts');
      assert(after.v2_helper_purchases === before.v2_helper_purchases,
        `self-purchase must create 0 helper purchases: before=${before.v2_helper_purchases}, after=${after.v2_helper_purchases}`);
      assert(after.v2_helper_invoices === before.v2_helper_invoices,
        `self-purchase must create 0 helper invoices: before=${before.v2_helper_invoices}, after=${after.v2_helper_invoices}`);
      assert(after.balance_provider_calls === before.balance_provider_calls,
        `self-purchase must make 0 balance-provider calls: before=${before.balance_provider_calls}, after=${after.balance_provider_calls}`);
      assert(after.helper_invoice_provider_calls === before.helper_invoice_provider_calls,
        `self-purchase must make 0 helper-invoice-provider calls: before=${before.helper_invoice_provider_calls}, after=${after.helper_invoice_provider_calls}`);
    });

    // ── Step 5e: hidden listing permits a brand-new paid create flow ──────────
    // then Client reactivates the ORIGINAL listing through owner mode (never
    // through /v2/new — create must never drive reactivation).
    await step('5e: Hidden listing does not block a new paid flow; owner-mode reactivation restores visibility', async () => {
      // External event: force listing to 'hidden' AND delete binding (binding TTL simulated)
      await devAPI(backendBase, 'POST', '/dev/listing/expire', { listing_id: listingId });

      // Same wallet, listing now hidden (not visible) → /v2/new must NOT block a
      // brand new paid flow (only an effectively VISIBLE listing blocks create).
      await withIncognito(async (tempPage) => {
        await tempPage.setViewportSize({ width: 390, height: 844 });
        await tempPage.goto(`${frontendBase}/v2/new`);
        await tempPage.waitForLoadState('networkidle');
        await tempPage.waitForSelector('input[type="text"]', { timeout: 10000 });
        await tempPage.fill('input[type="text"]', CLIENT_WALLET);
        await tempPage.waitForTimeout(300);
        await tempPage.click('button.btn-primary:not(:disabled)');
        // A NEW payment intent must be created (code gate reached), not a wallet_already_visible error.
        await tempPage.waitForSelector('.code-box', { timeout: 10000 });
        const errCount = await tempPage.locator('.err').count();
        assert(errCount === 0, 'hidden listing incorrectly blocked a brand-new paid create flow');
        // Abandon this throwaway second flow deliberately — no payment, no publish.
      });

      // Owner mode: Client's own browser revisits the (now hidden) listing.
      await clientPage.goto(`${frontendBase}/v2/listing/${listingId}`);
      await clientPage.waitForLoadState('networkidle');
      await clientPage.waitForSelector('[data-testid="owner-mode"]', { timeout: 10000 });
      const stateText = ((await clientPage.locator('[data-testid="owner-state"]').textContent()) || '').toLowerCase();
      assert(stateText.length > 0, 'owner state missing after expiry');

      // Telegram binding was deleted by /dev/listing/expire → must show connect button.
      await clientPage.waitForSelector('[data-testid="owner-connect-telegram-btn"]', { timeout: 10000 });
      await clientPage.click('[data-testid="owner-connect-telegram-btn"]');

      // Wait for the "open bot" link with a real bot URL.
      await clientPage.waitForSelector('a.owner-tg-btn[href*="t.me"]', { timeout: 15000 });
      const href = await clientPage.locator('a.owner-tg-btn[href*="t.me"]').first().getAttribute('href');
      assert(href, 'owner Telegram anchor has no href');
      const tokenMatch = href.match(/[?&]start=([^&]+)/);
      assert(tokenMatch, `no ?start= param in owner bot_url: ${href}`);
      await devAPI(backendBase, 'POST', '/dev/telegram/simulate-start', { raw_token: tokenMatch[1] });

      // Poll detects binding=ready → owner view refreshes → Reactivate button appears.
      await clientPage.waitForSelector('[data-testid="owner-reactivate-btn"]', { timeout: 15000 });
      await clientPage.click('[data-testid="owner-reactivate-btn"]');

      // Must transition back to visible.
      await clientPage.waitForFunction(
        () => (document.querySelector('[data-testid="owner-state"]')?.textContent || '').length > 0 &&
              !document.querySelector('[data-testid="owner-connect-telegram-btn"]') &&
              !document.querySelector('[data-testid="owner-reactivate-btn"]'),
        null,
        { timeout: 15000 }
      );

      // Listing must be back on the public board.
      const boardR = await fetch(`${frontendBase}/api/v2/board/tbilisi`);
      const board = await boardR.json();
      assert(Array.isArray(board) && board.find(l => l.id === listingId),
        'listing NOT back on board after owner-mode reactivation');

      await clientPage.screenshot({ path: join(SCREENSHOTS_DIR, `run${runNumber}_5e_reactivated.png`) });
    });

    // ── Step 6: Helper purchase → no ?pt= → invoice → clipboard ──────────────
    await step('6: Listing detail → helper wallet → purchase → URL has no ?pt= → invoice + clipboard', async () => {
      // Pre-set balance above dev helper floor ($60 pre-invoice, $50 post-payment)
      await devAPI(backendBase, 'POST', '/dev/balance/set', {
        wallet_address: HELPER_WALLET,
        balance_usd: 2000.0,
      });

      // Helper persona lives in its OWN browser context — never shares the
      // Client's localStorage (so owner mode never fires for the Helper).
      helperContext = await browser.newContext({ permissions: ['clipboard-read', 'clipboard-write'] });
      helperPage = await helperContext.newPage();
      helperPage.on('popup', p => { p.close().catch(() => {}); });
      await helperPage.setViewportSize({ width: 390, height: 844 });
      await helperPage.goto(`${frontendBase}/v2/listing/${listingId}`);
      await helperPage.waitForLoadState('networkidle');

      // Must be the ordinary public view, never owner mode.
      assert(await helperPage.locator('[data-testid="owner-mode"]').count() === 0,
        'Helper (separate device) incorrectly shows owner mode');

      // Informational notice must be visible before wallet input
      const noticeCount = await helperPage.locator('.notice-box, .notice-title').count();
      assert(noticeCount > 0, 'informational notice not shown on listing page');

      // Threshold hint must reflect public-config values (dev policy: pre-invoice = $60)
      const hintText = (await helperPage.locator('.hint').first().textContent().catch(() => ''));
      assert(hintText.includes('60') || hintText.includes('$60'),
        `threshold hint does not show dev pre-invoice min ($60): "${hintText}"`);

      // Fill helper wallet
      await helperPage.fill('input[type="text"]', HELPER_WALLET);
      await helperPage.waitForTimeout(500);

      // Capture API response for diagnostics
      let _apiRespBody = null;
      helperPage.on('response', async resp => {
        if (resp.url().includes('/api/v2/helper/contact-purchases') && resp.request().method() === 'POST') {
          try { _apiRespBody = await resp.json(); } catch {}
        }
      });

      // Click "Get contact" / helper purchase button
      await helperPage.click('button.btn-primary:not(:disabled)');
      await helperPage.waitForTimeout(2000);

      // URL must NOT expose ?pt=
      const url = helperPage.url();
      assert(!url.includes('?pt=') && !url.includes('&pt=') && !url.includes('%3Fpt%3D'),
        `purchase token exposed in URL: ${url}`);
      const _errText = await helperPage.locator('.err').first().textContent().catch(() => '');
      assert(url.includes('/v2/helper/purchase'),
        `expected /v2/helper/purchase, got: ${url}; err="${_errText}"; API resp: ${JSON.stringify(_apiRespBody)}`);

      // Invoice must be visible
      await helperPage.waitForSelector('.pay-surface', { timeout: 15000 });

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

    // ── Step 6g: Cross-device handoff — full threat matrix ────────────────────
    await step('6g: Cross-device handoff — wrong wallet, expiry, concurrent redeem, real redeem, old-token revoked, replay', async () => {
      // helperPage is on /v2/helper/purchase (invoice step) from step 6.
      await helperPage.waitForSelector('.btn-handoff', { timeout: 5000 });

      function assertSafeHandoffFailure(status, body, label) {
        assert(status === 404, `${label}: expected safe 404, got ${status}`);
        assert(body && body.error === 'purchase not found' && body.code === 'purchase_not_found',
          `${label}: unexpected safe error body ${JSON.stringify(body)}`);
        assert(Object.keys(body).length === 2,
          `${label}: safe error leaked additional fields ${JSON.stringify(body)}`);
      }

      async function responseJSON(resp, label) {
        const text = await resp.text();
        try {
          return JSON.parse(text);
        } catch {
          throw new Error(`${label}: response is not JSON: status=${resp.status()} body=${JSON.stringify(text)}`);
        }
      }

      async function createHandoffFromHelperPage() {
        const [resp] = await Promise.all([
          helperPage.waitForResponse(r => r.url().includes('/api/v2/helper/handoff/create') && r.request().method() === 'POST'),
          helperPage.click('.btn-handoff'),
        ]);
        const data = await resp.json();
        assert(data && data.token, 'handoff create response missing token');
        // Token travels ONLY in the URL fragment — never a query param, never the wallet.
        const url = `${frontendBase}/v2/helper/purchase#handoff=${encodeURIComponent(data.token)}`;
        assert(!url.includes('wallet='), 'handoff URL must never contain a wallet param');
        return url;
      }

      // UI verification: box, QR, expiry, close button all present.
      const url1 = await createHandoffFromHelperPage();
      await helperPage.waitForSelector('.handoff-box', { timeout: 10000 });
      const qrInBox = await helperPage.locator('.handoff-qr .v2qr svg, .handoff-qr svg').count();
      assert(qrInBox > 0, 'V2QR SVG not rendered inside .handoff-qr');
      assert(await helperPage.locator('.handoff-expires').count() > 0, '.handoff-expires element not found');
      assert(await helperPage.locator('.handoff-box .btn-text').count() > 0, 'close button (.btn-text) not found');
      const invoiceAddr = (await helperPage.locator('.inv-val.addr').first().textContent().catch(() => '')).trim();
      assert(invoiceAddr.length > 0, 'cannot read invoice address before handoff redeem tests');
      await helperPage.locator('.handoff-box .btn-text').first().click();
      await helperPage.waitForTimeout(300);

      // ── Negative: wrong wallet — token still pending afterward (no consume on failure) ──
      await withIncognito(async (wrongPage) => {
        await wrongPage.setViewportSize({ width: 390, height: 844 });
        await wrongPage.goto(url1);
        await wrongPage.waitForLoadState('networkidle');
        // Must show the dedicated redeem form — NOT auto-submitted, NOT pre-filled.
        await wrongPage.waitForSelector('[data-testid="handoff-redeem-form"]', { timeout: 10000 });
        const prefilled = await wrongPage.locator('[data-testid="handoff-redeem-wallet-input"]').inputValue();
        assert(prefilled === '', `redeem wallet field must start empty, got "${prefilled}"`);
        assert(await wrongPage.locator('.pay-surface').count() === 0, 'invoice rendered before redeem submit — must not auto-submit');
        await wrongPage.fill('[data-testid="handoff-redeem-wallet-input"]', CLIENT_WALLET); // wrong wallet
        const [wrongResp] = await Promise.all([
          wrongPage.waitForResponse(r => r.url().includes('/api/v2/helper/handoff/redeem') && r.request().method() === 'POST'),
          wrongPage.click('[data-testid="handoff-redeem-submit-btn"]'),
        ]);
        const wrongBody = await responseJSON(wrongResp, 'wrong-wallet redeem');
        assertSafeHandoffFailure(wrongResp.status(), wrongBody, 'wrong-wallet redeem');
        await wrongPage.waitForTimeout(1500);
        const errText = (await wrongPage.locator('.err').textContent().catch(() => ''));
        assert(errText.length > 0, 'wrong-wallet handoff redeem must show an error');
        assert(await wrongPage.locator('.pay-surface').count() === 0, 'wrong-wallet redeem must not restore the invoice');
      });

      // ── Negative: expired token — same safe response, no invoice restore ──
      // Wrong-wallet validation does not consume the capability, so reuse url1
      // for the expiry boundary and stay inside the production create limiter.
      await devAPI(backendBase, 'POST', '/dev/helper/handoff/expire', { purchase_id: purchaseId });
      await withIncognito(async (expiredPage) => {
        await expiredPage.setViewportSize({ width: 390, height: 844 });
        await expiredPage.goto(url1);
        await expiredPage.waitForLoadState('networkidle');
        await expiredPage.waitForSelector('[data-testid="handoff-redeem-form"]', { timeout: 10000 });
        await expiredPage.fill('[data-testid="handoff-redeem-wallet-input"]', HELPER_WALLET);
        const [expiredResp] = await Promise.all([
          expiredPage.waitForResponse(r => r.url().includes('/api/v2/helper/handoff/redeem') && r.request().method() === 'POST'),
          expiredPage.click('[data-testid="handoff-redeem-submit-btn"]'),
        ]);
        const expiredBody = await responseJSON(expiredResp, 'expired redeem');
        assertSafeHandoffFailure(expiredResp.status(), expiredBody, 'expired redeem');
        assert(await expiredPage.locator('.pay-surface').count() === 0,
          'expired handoff token must not restore the invoice');
      });

      // ── Concurrent real cross-device redeem — exactly one winner ─────────
      const url2 = await createHandoffFromHelperPage();
      await helperPage.locator('.handoff-box .btn-text').first().click();
      await helperPage.waitForTimeout(300);
      const staleToken = purchaseToken;
      let newDeviceToken = '';
      const raceContexts = await Promise.all([
        browser.newContext({ permissions: ['clipboard-read', 'clipboard-write'] }),
        browser.newContext({ permissions: ['clipboard-read', 'clipboard-write'] }),
      ]);
      scratchContexts.push(...raceContexts);
      try {
        const racePages = await Promise.all(raceContexts.map(ctx => ctx.newPage()));
        await Promise.all(racePages.map(async page => {
          await page.setViewportSize({ width: 390, height: 844 });
          await page.goto(url2);
          await page.waitForLoadState('networkidle');
          await page.waitForSelector('[data-testid="handoff-redeem-form"]', { timeout: 10000 });
          const prefilled = await page.locator('[data-testid="handoff-redeem-wallet-input"]').inputValue();
          assert(prefilled === '', `concurrent redeem wallet field must start empty, got "${prefilled}"`);
          await page.fill('[data-testid="handoff-redeem-wallet-input"]', HELPER_WALLET);
        }));

        const responsePromises = racePages.map(page =>
          page.waitForResponse(r => r.url().includes('/api/v2/helper/handoff/redeem') && r.request().method() === 'POST')
        );
        await Promise.all(racePages.map(page => page.click('[data-testid="handoff-redeem-submit-btn"]')));
        const responses = await Promise.all(responsePromises);
        const raceResults = await Promise.all(responses.map(async (resp, index) => ({
          index,
          status: resp.status(),
          body: await responseJSON(resp, `concurrent redeem ${index + 1}`),
        })));
        const winners = raceResults.filter(r => r.status === 200 && r.body && r.body.browser_token);
        const losers = raceResults.filter(r => r.status !== 200);
        assert(winners.length === 1, `concurrent redeem: want exactly 1 winner, got ${winners.length}`);
        assert(losers.length === 1, `concurrent redeem: want exactly 1 loser, got ${losers.length}`);
        assertSafeHandoffFailure(losers[0].status, losers[0].body, 'concurrent redeem loser');

        newDeviceToken = winners[0].body.browser_token;
        const winningPage = racePages[winners[0].index];
        try {
          await winningPage.waitForSelector('.pay-surface', { timeout: 15000 });
        } catch (e) {
          const ui = await winningPage.locator('body').innerText().catch(() => '');
          throw new Error(`winning device did not restore invoice after 200 redeem; body=${JSON.stringify(ui.slice(0, 1000))}; ${e.message}`);
        }
        const redeemAddr = (await winningPage.locator('.inv-val.addr').first().textContent().catch(() => '')).trim();
        assert(redeemAddr === invoiceAddr,
          `handoff redeem shows wrong invoice address: got "${redeemAddr}", expected "${invoiceAddr}"`);
        await winningPage.screenshot({ path: join(SCREENSHOTS_DIR, `run${runNumber}_6g_handoff.png`) });

        const losingPage = racePages[losers[0].index];
        await losingPage.waitForTimeout(800);
        assert(await losingPage.locator('.pay-surface').count() === 0,
          'concurrent redeem loser must not restore the invoice');
      } finally {
        await Promise.all(raceContexts.map(ctx => ctx.close().catch(() => {})));
        for (const ctx of raceContexts) {
          const idx = scratchContexts.indexOf(ctx);
          if (idx >= 0) scratchContexts.splice(idx, 1);
        }
      }

      // Old (pre-redeem) token must be revoked immediately: helperPage still
      // holds `staleToken` — reloading with it must now fail.
      await helperPage.evaluate((tok) => {
        try { sessionStorage.setItem('v2_active_purchase_token', tok); } catch {}
      }, staleToken);
      await helperPage.reload();
      await helperPage.waitForLoadState('networkidle');
      const revokedOk = await helperPage.locator('.pay-surface').count();
      assert(revokedOk === 0, 'old browser token still works after handoff redeem — must be revoked');

      // ── Negative: replay — reusing url2's already-consumed token must fail ──
      await withIncognito(async (replayPage) => {
        await replayPage.setViewportSize({ width: 390, height: 844 });
        await replayPage.goto(url2);
        await replayPage.waitForLoadState('networkidle');
        await replayPage.waitForSelector('[data-testid="handoff-redeem-form"]', { timeout: 10000 });
        await replayPage.fill('[data-testid="handoff-redeem-wallet-input"]', HELPER_WALLET);
        const [replayResp] = await Promise.all([
          replayPage.waitForResponse(r => r.url().includes('/api/v2/helper/handoff/redeem') && r.request().method() === 'POST'),
          replayPage.click('[data-testid="handoff-redeem-submit-btn"]'),
        ]);
        const replayBody = await responseJSON(replayResp, 'replayed redeem');
        assertSafeHandoffFailure(replayResp.status(), replayBody, 'replayed redeem');
        await replayPage.waitForTimeout(1500);
        assert(await replayPage.locator('.pay-surface').count() === 0, 'replayed handoff token must not restore the invoice');
        const errText = (await replayPage.locator('.err').textContent().catch(() => ''));
        assert(errText.length > 0, 'replayed handoff token must show an error');
      });

      // Restore continuity on helperPage using the new valid token from device 2,
      // so steps 6b onward continue operating on a working session.
      await helperPage.evaluate(({ tok, wallet, lid }) => {
        try { sessionStorage.setItem('v2_active_purchase_token', tok); } catch {}
        try {
          const saved = { token: tok, wallet, listingId: lid, city: 'tbilisi' };
          localStorage.setItem('v2_active_hpt', JSON.stringify(saved));
          localStorage.setItem(`v2_hpt_${lid}`, JSON.stringify(saved));
        } catch {}
      }, { tok: newDeviceToken, wallet: HELPER_WALLET, lid: listingId });
      const restoreResponsePromise = helperPage.waitForResponse(r =>
        r.url().includes('/api/v2/helper/contact-purchases/restore') && r.request().method() === 'POST'
      );
      await helperPage.reload();
      const rotatedRestoreResp = await restoreResponsePromise;
      const rotatedRestoreBody = await responseJSON(rotatedRestoreResp, 'rotated-session restore');
      const rotatedRestoreRequest = rotatedRestoreResp.request().postData() || '';
      assert(rotatedRestoreResp.ok(),
        `rotated-session restore failed: status=${rotatedRestoreResp.status()} request=${rotatedRestoreRequest} body=${JSON.stringify(rotatedRestoreBody)}`);
      await helperPage.waitForLoadState('networkidle');
      try {
        await helperPage.waitForSelector('.pay-surface', { timeout: 15000 });
      } catch (e) {
        const ui = await helperPage.locator('body').innerText().catch(() => '');
        throw new Error(`original device did not restore rotated session; body=${JSON.stringify(ui.slice(0, 1000))}; ${e.message}`);
      }
      const finalAddr = (await helperPage.locator('.inv-val.addr').first().textContent().catch(() => '')).trim();
      assert(finalAddr === invoiceAddr, 'invoice address changed after restoring continuity — wrong purchase');
      purchaseToken = newDeviceToken;
    });

    // ── Step 6b: Continue purchase — board badge, listing Continue, same invoice ─
    await step('6b: Continue purchase — board shows badge, listing shows Continue, same invoice restored', async () => {
      // Capture invoice address before navigating away
      const invoiceAddr = (await helperPage.locator('.inv-val.addr').first().textContent().catch(() => '')).trim();
      assert(invoiceAddr.length > 0, 'could not read invoice address before continue-purchase test');

      // The saved purchase belongs to the Helper browser context, not Client.
      await helperPage.goto(`${frontendBase}/v2/board/tbilisi`);
      await helperPage.waitForLoadState('networkidle');
      await helperPage.waitForSelector('.card.listing', { timeout: 10000 });
      await helperPage.waitForSelector('.continue-badge', { timeout: 10000 });
      const badgeCount = await helperPage.locator('.continue-badge').count();
      assert(badgeCount > 0, 'continue-badge not shown on board for listing with pending purchase');

      // Simulate new-session restore: clear helperPage sessionStorage
      await helperPage.evaluate(() => { try { sessionStorage.clear(); } catch {} });

      // Navigate to listing — localStorage restore kicks in → should show Continue purchase
      await helperPage.goto(`${frontendBase}/v2/listing/${listingId}`);
      await helperPage.waitForLoadState('networkidle');
      await helperPage.waitForSelector('.state-box', { timeout: 15000 });
      const continueLinkCount = await helperPage.locator('a.btn-link').count();
      assert(continueLinkCount > 0, 'Continue purchase link not found on listing page after sessionStorage clear');

      // Wallet form must NOT be shown (purchase already exists)
      const walletFormCount = await helperPage.locator('.field input[type="text"]').count();
      assert(walletFormCount === 0, 'wallet form shown instead of Continue purchase — localStorage restore failed');

      // Click Continue → /v2/helper/purchase → same invoice
      await helperPage.locator('a.btn-link').first().click();
      await helperPage.waitForLoadState('networkidle');
      await helperPage.waitForSelector('.pay-surface', { timeout: 15000 });

      // Invoice address must be identical — no duplicate invoice created
      const newAddr = (await helperPage.locator('.inv-val.addr').first().textContent().catch(() => '')).trim();
      assert(newAddr === invoiceAddr,
        `invoice address changed — duplicate invoice created? got "${newAddr}", expected "${invoiceAddr}"`);

      await helperPage.screenshot({ path: join(SCREENSHOTS_DIR, `run${runNumber}_6b_continue.png`) });
    });

    // ── Step 6v: Layout acceptance — 5 viewports ─────────────────────────────
    await step('6v: Layout acceptance — scroll/visibility/overlap on 5 viewports', async () => {
      // helperPage is on /v2/helper/purchase (awaiting_payment) from step 6b
      const VIEWPORTS = [
        { w: 1440, h: 900  },
        { w: 1280, h: 720  },
        { w: 390,  h: 844  },
        { w: 375,  h: 667  },
        { w: 360,  h: 640  },
      ];

      for (const vp of VIEWPORTS) {
        await helperPage.setViewportSize({ width: vp.w, height: vp.h });
        await helperPage.waitForTimeout(600);

        // 1. No document scroll
        const scroll = await helperPage.evaluate(() => ({
          sH: document.documentElement.scrollHeight,
          cH: document.documentElement.clientHeight,
          sW: document.documentElement.scrollWidth,
          cW: document.documentElement.clientWidth,
        }));
        assert(scroll.sH <= scroll.cH,
          `${vp.w}×${vp.h}: scrollHeight ${scroll.sH} > clientHeight ${scroll.cH}`);
        assert(scroll.sW <= scroll.cW,
          `${vp.w}×${vp.h}: scrollWidth ${scroll.sW} > clientWidth ${scroll.cW}`);

        // Helper: assert element visible and bounding box fully within viewport
        async function assertIn(sel, label) {
          const el = helperPage.locator(sel).first();
          assert(await el.isVisible(), `${vp.w}×${vp.h}: ${label} not visible`);
          const box = await el.boundingBox();
          assert(box !== null, `${vp.w}×${vp.h}: ${label} bbox null`);
          assert(box.x >= -1, `${vp.w}×${vp.h}: ${label} left=${box.x.toFixed(0)} < 0`);
          assert(box.y >= -1, `${vp.w}×${vp.h}: ${label} top=${box.y.toFixed(0)} < 0`);
          assert(box.x + box.width  <= vp.w + 1,
            `${vp.w}×${vp.h}: ${label} right=${( box.x + box.width ).toFixed(0)} > ${vp.w}`);
          assert(box.y + box.height <= vp.h + 1,
            `${vp.w}×${vp.h}: ${label} bottom=${( box.y + box.height).toFixed(0)} > ${vp.h}`);
          return box;
        }

        function overlaps(a, b) {
          if (!a || !b) return false;
          return !(a.x + a.width <= b.x || b.x + b.width <= a.x ||
                   a.y + a.height <= b.y || b.y + b.height <= a.y);
        }

        // 2. Required elements — visible and within viewport
        const statusBox = await assertIn('.inv-status',          'status');
        const amountBox = await assertIn('.pay-surface .inv-val','amount');
        const addrBox   = await assertIn('.inv-val.addr',        'address');
        const aliasBox  = await assertIn('.alias-block',         'aliases');
        const instrBox  = await assertIn('.instr-block',         'instructions');
        const topbarBox = await assertIn('.topbar',               'topbar');
        const navBox    = await assertIn('.topbar-nav',           'topbar navigation');
        const progBox   = await assertIn('.progress',             'progress');
        await assertIn('.instr',                                  'instr line');

        // All 3 instruction lines present (not hidden)
        const instrCount = await helperPage.locator('.instr').count();
        assert(instrCount === 3,
          `${vp.w}×${vp.h}: expected 3 .instr lines, got ${instrCount}`);

        // 3. QR: visible, >= 112×112, bottom within viewport
        await helperPage.locator('.v2qr svg').first().waitFor({ state: 'visible', timeout: 5000 });
        const qrBox = await assertIn('.v2qr svg', 'QR svg');
        assert(qrBox.width  >= 112, `${vp.w}×${vp.h}: QR width  ${qrBox.width.toFixed(0)} < 112`);
        assert(qrBox.height >= 112, `${vp.w}×${vp.h}: QR height ${qrBox.height.toFixed(0)} < 112`);

        // 4. Font sizes — main text >= 13px, labels >= 11px, instructions >= 11px
        const mainFs = await helperPage.locator('.inv-val').first()
          .evaluate(el => parseFloat(getComputedStyle(el).fontSize));
        assert(mainFs >= 13, `${vp.w}×${vp.h}: inv-val font ${mainFs}px < 13px`);

        const labelFs = await helperPage.locator('.inv-label').first()
          .evaluate(el => parseFloat(getComputedStyle(el).fontSize));
        assert(labelFs >= 11, `${vp.w}×${vp.h}: inv-label font ${labelFs}px < 11px`);

        const instrFs = await helperPage.locator('.instr').first()
          .evaluate(el => parseFloat(getComputedStyle(el).fontSize));
        assert(instrFs >= 11, `${vp.w}×${vp.h}: instr font ${instrFs}px < 11px`);

        // 5. No overlaps: alias-block ∩ pay-surface, instr-block ∩ pay-surface
        const payBox = await helperPage.locator('.pay-surface').first().boundingBox();
        assert(!overlaps(aliasBox, payBox),
          `${vp.w}×${vp.h}: alias-block overlaps pay-surface`);
        assert(!overlaps(instrBox, payBox),
          `${vp.w}×${vp.h}: instr-block overlaps pay-surface`);
        assert(navBox.y >= topbarBox.y - 1 &&
               navBox.y + navBox.height <= topbarBox.y + topbarBox.height + 1,
          `${vp.w}×${vp.h}: topbar navigation escapes topbar; nav=${JSON.stringify(navBox)} topbar=${JSON.stringify(topbarBox)}`);
        assert(!overlaps(navBox, progBox),
          `${vp.w}×${vp.h}: topbar navigation overlaps progress`);
        assert(!overlaps(navBox, aliasBox),
          `${vp.w}×${vp.h}: topbar navigation overlaps aliases`);

        // 6. Language switcher (if present) must not overlap pay-surface
        const langCount = await helperPage.locator('.lang-bar').count();
        if (langCount > 0) {
          const langBox = await helperPage.locator('.lang-bar').first().boundingBox();
          assert(!overlaps(langBox, payBox),
            `${vp.w}×${vp.h}: lang switcher overlaps pay-surface; lang=${JSON.stringify(langBox)} pay=${JSON.stringify(payBox)}`);
        }

        await helperPage.screenshot({
          path: join(SCREENSHOTS_DIR, `run${runNumber}_layout_${vp.w}x${vp.h}.png`),
        });
      }

      // Restore viewport for subsequent steps
      await helperPage.setViewportSize({ width: 390, height: 844 });
      await helperPage.waitForTimeout(300);
    });

    // ── Step 6c: Expired purchase — terminal screen, no redirect loop, new purchase ─
    await step('6c: Expired invoice → terminal screen → back to board → no loop → new purchase + new invoice', async () => {
      // Capture the current invoice address before expiring
      const expiredAddr = (await helperPage.locator('.inv-val.addr').first().textContent().catch(() => '')).trim();
      assert(expiredAddr.length > 0, 'could not read invoice address before expire test');

      // External event: force invoice to expired state
      await devAPI(backendBase, 'POST', '/dev/helper/invoice/expire', { purchase_id: purchaseId });

      // Simulate "closed tab": clear sessionStorage so the purchase page must load from localStorage
      await helperPage.evaluate(() => { try { sessionStorage.clear(); } catch {} });

      // Navigate to /v2/helper/purchase — localStorage has expired token → restore → terminal
      await helperPage.goto(`${frontendBase}/v2/helper/purchase`);
      await helperPage.waitForLoadState('networkidle');

      // Terminal screen must be shown (step === 'done')
      await helperPage.waitForSelector('a.btn-secondary[href*="/v2/board"]', { timeout: 10000 });

      // Error must mention expiry
      const errText = (await helperPage.locator('.err').textContent().catch(() => '')).trim();
      assert(errText.toLowerCase().includes('expired'),
        `expected expiry message on terminal screen, got: "${errText}"`);

      // Still on /v2/helper/purchase (not looped back yet)
      assert(helperPage.url().includes('/v2/helper/purchase'),
        `expected /v2/helper/purchase, got: ${helperPage.url()}`);

      await helperPage.screenshot({ path: join(SCREENSHOTS_DIR, `run${runNumber}_6c_terminal.png`) });

      // Click "Back to board" — storage is already cleared by clearHelperPurchaseState
      await helperPage.click('a.btn-secondary[href*="/v2/board"]');
      await helperPage.waitForURL(`**\/v2\/board\/**`, { timeout: 8000 });
      assert(helperPage.url().includes('/v2/board'),
        `expected /v2/board after back-to-board, got: ${helperPage.url()}`);

      // Wait and confirm no redirect loop back to purchase
      await helperPage.waitForTimeout(1500);
      assert(!helperPage.url().includes('/v2/helper/purchase'),
        `redirect loop detected — bounced back to purchase from board: ${helperPage.url()}`);

      // Navigate to listing — must show wallet form (stale state-box must be gone)
      await helperPage.goto(`${frontendBase}/v2/listing/${listingId}`);
      await helperPage.waitForLoadState('networkidle');
      await helperPage.waitForTimeout(1500); // allow async restore check to complete

      // Wallet input must be visible — expired purchase was cleared
      const walletFieldCount = await helperPage.locator('.field input[type="text"]').count();
      assert(walletFieldCount > 0,
        'wallet input not shown after expired purchase — stale state-box or continue badge still present');

      // state-box (Continue purchase) must NOT be shown
      const stateBoxCount = await helperPage.locator('.state-box').count();
      assert(stateBoxCount === 0,
        'state-box still visible after expired purchase cleared — stale localStorage not removed');

      // Create new purchase for the same wallet
      await helperPage.fill('input[type="text"]', HELPER_WALLET);
      await helperPage.waitForTimeout(300);
      const [createAgainResp] = await Promise.all([
        helperPage.waitForResponse(r => r.url().includes('/api/v2/helper/contact-purchases') && r.request().method() === 'POST'),
        helperPage.click('button.btn-primary:not(:disabled)'),
      ]);
      assert(createAgainResp.ok(),
        `new purchase after expiry failed: status=${createAgainResp.status()}`);
      await helperPage.waitForURL(`**\/v2\/helper\/purchase**`, { timeout: 10000 });
      await helperPage.waitForSelector('.pay-surface', { timeout: 10000 });

      // New invoice must be visible
      const newAddr = (await helperPage.locator('.inv-val.addr').first().textContent().catch(() => '')).trim();
      assert(newAddr.length > 0, 'new invoice address is empty');

      // Update purchaseToken and purchaseId BEFORE further assertions
      // so step 7 always operates on the new purchase.
      const oldPurchaseId = purchaseId;
      purchaseToken = await helperPage.evaluate(() => {
        try { return sessionStorage.getItem('v2_active_purchase_token') || ''; } catch { return ''; }
      });
      assert(purchaseToken.length > 0, 'new purchaseToken not in sessionStorage after new purchase');

      const newSaved = await helperPage.evaluate((pt) => {
        try { return JSON.parse(sessionStorage.getItem(`v2_purchase_${pt}`) || 'null'); } catch { return null; }
      }, purchaseToken);
      assert(newSaved && newSaved.purchaseId, 'new purchaseId not found in sessionStorage');
      purchaseId = newSaved.purchaseId;

      // New purchase must have a different ID — verifies a fresh purchase was created
      assert(purchaseId !== oldPurchaseId,
        `new purchase has same ID as expired one — backend allowed reuse: "${purchaseId}"`);
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
      // Unlock review: zero out available_at so review buttons appear immediately
      // (RevealHelperContact sets available_at = reveal_time + 3600; bypass for E2E).
      await devAPI(backendBase, 'POST', '/dev/helper/review/unlock', { purchase_id: purchaseId });

      // Reload so the frontend re-fetches review capability with available_at = 0.
      await helperPage.reload();
      await helperPage.waitForLoadState('networkidle');

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
      const notificationList = notifs.notifications || [];
      assert(notificationList.some(item => item.text && !item.pos_data),
        'immediate purchase notification was not delivered');
      const n = notificationList.find(item => item.pos_data);
      assert(n && n.pos_data, 'no review notification with pos_data found after helper review');

      // Verify notification message text contains Helper info and rating symbols.
      assert(n.text && n.text.length > 0, 'review notification text must not be empty');
      assert(n.text.includes('Helper:'), `notification text missing "Helper:" label: ${n.text}`);
      assert(n.text.includes('👍') && n.text.includes('👎'),
        `notification text missing 👍/👎 rating symbols: ${n.text}`);
      assert(n.text.includes('Helper help you?'),
        `notification text missing review question: ${n.text}`);

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

    // ── Step 10: /v2/restore matrix — wrong/mixed/unknown identical error; valid → owner mode ─
    await step('10: /v2/restore — wrong/mixed/unknown give identical error; valid code+wallet → owner mode', async () => {
      await withIncognito(async (restorePage) => {
        await restorePage.setViewportSize({ width: 390, height: 844 });

        async function tryRestore(wallet, code) {
          await restorePage.goto(`${frontendBase}/v2/restore`);
          await restorePage.waitForLoadState('networkidle');
          await restorePage.fill('input[type="text"]', wallet);
          await restorePage.fill('input.code-input', code);
          await restorePage.click('button.btn-primary:not(:disabled)');
          await restorePage.waitForSelector('.err', { timeout: 10000 });
          return (await restorePage.locator('.err').textContent()) || '';
        }

        // Wrong code, correct wallet.
        const errWrongCode = await tryRestore(CLIENT_WALLET, 'wrong-code-0000000000000000');
        // Correct code, wrong wallet.
        const errWrongWallet = await tryRestore(HELPER_WALLET, managementCode);
        // Mixed pair from two different real listings (still both "unknown" together): use a
        // syntactically-valid but entirely unrelated wallet + unrelated code.
        const errUnknown = await tryRestore('bc1qxy2kgdygjrsqtzq2n0yrf2493p83kkfjhx0wlh', 'totally-unknown-code-000000');

        assert(errWrongCode.length > 0 && errWrongCode === errWrongWallet && errWrongWallet === errUnknown,
          `restore errors must be byte-identical: wrongCode="${errWrongCode}" wrongWallet="${errWrongWallet}" unknown="${errUnknown}"`);

        await restorePage.screenshot({ path: join(SCREENSHOTS_DIR, `run${runNumber}_10_restore_error.png`) });

        // Valid pair → owner mode on the listing page (never /v2/new).
        await restorePage.goto(`${frontendBase}/v2/restore`);
        await restorePage.waitForLoadState('networkidle');
        await restorePage.fill('input[type="text"]', CLIENT_WALLET);
        await restorePage.fill('input.code-input', managementCode);
        await restorePage.click('button.btn-primary:not(:disabled)');
        await restorePage.waitForURL(`**\/v2\/listing\/${listingId}`, { timeout: 10000 });
        await restorePage.waitForSelector('[data-testid="owner-mode"]', { timeout: 10000 });
        assert(!restorePage.url().includes('/v2/new'), 'restore must never navigate through /v2/new');

        await restorePage.screenshot({ path: join(SCREENSHOTS_DIR, `run${runNumber}_10_restore_owner.png`) });
      });
    });

  } finally {
    // Close both persistent pages/contexts
    if (clientPage) { try { await clientPage.close(); } catch {} }
    if (helperPage) { try { await helperPage.close(); } catch {} }
    if (helperContext) { try { await helperContext.close(); } catch {} }
    for (const ctx of scratchContexts.slice()) { try { await ctx.close(); } catch {} }
    await context.close();
    await browser.close();
    await teardown(env);
  }

  // Verify all screenshot files exist and have non-zero size
  const screenshots = [
    '01_invoice', '04_done', '04b_owner_mode', '05_board_desktop', '05_board_mobile',
    '5b_outage', '5c_wallet_already', '5d_self_purchase', '5e_reactivated',
    '06_helper_invoice', '6g_handoff', '6b_continue',
    'layout_1440x900', 'layout_1280x720', 'layout_390x844', 'layout_375x667', 'layout_360x640',
    '6c_terminal', '07_contact', '09_review', '10_restore_error', '10_restore_owner',
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
    results.push({ passed: 0, failed: 20, failures: [{ name: 'fatal', error: e.message }] });
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
