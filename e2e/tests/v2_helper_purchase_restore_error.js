#!/usr/bin/env node
/**
 * Focused regression test — /v2/helper/purchase restore error-state fix.
 *
 * Scope: ONLY the restore error-classification fix in
 * frontend/src/routes/v2/helper/purchase/+page.svelte (FIX_RU + FIX2_RU).
 * Does not exercise the rest of the Helper purchase journey.
 *
 * Starts ONLY the Vite frontend dev server (no Go backend needed) on a
 * dynamic port. The restore call itself is intercepted via Playwright
 * page.route(), so no real backend state is required for any of the ten
 * cases below — this keeps the test fast and deterministic.
 *
 * Cases (must all pass; numbering matches NA_ROOM_V2_CLAUDE_RELEASE_FIX2_RU.md):
 *   1. network failure (route.abort())               → error-state + Retry, storage preserved
 *   2. timeout/abort (client-side 10s AbortController) → error-state + Retry
 *   3. HTTP 500                                        → error-state + Retry, raw error hidden
 *   4. HTTP 429                                         → error-state + Retry, raw error hidden
 *   5. malformed JSON body                              → error-state + Retry, no raw parse error
 *   6. HTTP 404 code=purchase_not_found                 → no_purchase message, NO Retry,
 *                                                          stale continuity state cleared
 *   7. other 4xx (e.g. 403)                             → safe generic message, raw body hidden, NO Retry
 *   8. Retry after a transient failure resumes the SAME purchase and never
 *      calls the create-invoice endpoint
 *   9. terminal successful response (200 + phase) still goes through the
 *      EXISTING, untouched routeByPhase() logic
 *  10. Listing/Board links are present, correct, and navigable from the error-state
 */

import { chromium } from 'playwright';
import { spawn } from 'child_process';
import { resolve } from 'path';
import net from 'net';

const ROOT = resolve(new URL('../../', import.meta.url).pathname);

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

async function findFreePort() {
  return new Promise((resolveP, reject) => {
    const srv = net.createServer();
    srv.listen(0, '127.0.0.1', () => {
      const port = srv.address().port;
      srv.close(() => resolveP(port));
    });
    srv.on('error', reject);
  });
}

async function isPortOpen(port) {
  async function tryHost(host) {
    return new Promise(resolveP => {
      const c = net.createConnection(port, host);
      c.setTimeout(500);
      c.on('connect', () => { c.destroy(); resolveP(true); });
      c.on('error', () => resolveP(false));
      c.on('timeout', () => { c.destroy(); resolveP(false); });
    });
  }
  return (await tryHost('127.0.0.1')) || (await tryHost('::1'));
}

async function waitForPort(port, { timeout = 60000, label = '' } = {}) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (await isPortOpen(port)) return;
    await sleep(300);
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

function assert(cond, msg) {
  if (!cond) throw new Error(msg);
}

const TOKEN = 'focusedtest0000000000000000000000000000000000000000000000000t';
const WALLET = 'bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq';
const LISTING_ID = 'mock-listing-restore-test';
const CITY = 'tbilisi';

async function seedStorage(page) {
  await page.addInitScript(({ token, wallet, listingId, city, purchaseId }) => {
    try {
      sessionStorage.setItem('v2_active_purchase_token', token);
      sessionStorage.setItem(`v2_purchase_${token}`, JSON.stringify({ purchaseId, listingId, walletAddress: wallet, city }));
      localStorage.setItem('v2_active_hpt', JSON.stringify({ token, wallet, listingId, city }));
    } catch {}
  }, { token: TOKEN, wallet: WALLET, listingId: LISTING_ID, city: CITY, purchaseId: 'mock-purchase-id-1' });
}

async function storageSnapshot(page) {
  return page.evaluate(() => {
    try {
      return {
        sessionToken: sessionStorage.getItem('v2_active_purchase_token'),
        activeHpt: localStorage.getItem('v2_active_hpt'),
      };
    } catch { return { sessionToken: null, activeHpt: null }; }
  });
}

async function startFrontend() {
  const frontendPort = await findFreePort();
  const viteEntry = resolve(ROOT, 'frontend', 'node_modules', 'vite', 'bin', 'vite.js');
  const frontend = spawn(
    process.execPath,
    [viteEntry, 'dev', '--port', String(frontendPort)],
    {
      cwd: resolve(ROOT, 'frontend'),
      env: { ...process.env, NO_COLOR: '1' },
      stdio: ['ignore', 'pipe', 'pipe'],
    }
  );
  frontend._lines = [];
  frontend.stdout.on('data', d => frontend._lines.push(...d.toString().split('\n')));
  frontend.stderr.on('data', d => frontend._lines.push(...d.toString().split('\n')));
  await waitForPort(frontendPort, { label: 'frontend', timeout: 60000 });
  const frontendBase = `http://localhost:${frontendPort}`;
  console.log(`  frontend: ${frontendBase}`);
  return { frontend, frontendPort, frontendBase };
}

async function teardown(env) {
  try { env.frontend.kill('SIGKILL'); } catch {}
  await sleep(800);
  await assertPortClosed(env.frontendPort, { timeout: 5000 });
  console.log(`  ✓ port ${env.frontendPort} released`);
}

const RESTORE_URL = '**/api/v2/helper/contact-purchases/restore';
const CREATE_URL = '**/api/v2/helper/contact-purchases';

async function runOnce(runNumber) {
  const env = await startFrontend();
  const browser = await chromium.launch();
  try {
    const results = [];

    // ── Case 1: network failure (route.abort) ────────────────────────────────
    {
      const context = await browser.newContext();
      const page = await context.newPage();
      await seedStorage(page);
      await page.route(RESTORE_URL, route => route.abort('failed'));
      await page.goto(`${env.frontendBase}/v2/helper/purchase`);
      await page.waitForSelector('[data-testid="restore-error-state"]', { timeout: 10000 });
      const errText = (await page.locator('.err').first().textContent().catch(() => '')).trim();
      assert(errText.length > 0, 'case 1: no visible error message (blank page regression)');
      assert(await page.locator('[data-testid="restore-retry-btn"]').count() === 1, 'case 1: retry button missing');
      const snap = await storageSnapshot(page);
      assert(snap.sessionToken === TOKEN && !!snap.activeHpt, 'case 1: storage was cleared on a transient failure');
      results.push('1: network failure → error-state + Retry, storage preserved');
      await context.close();
    }

    // ── Case 2: timeout/abort (client-side 10s AbortController) ──────────────
    {
      const context = await browser.newContext();
      const page = await context.newPage();
      await seedStorage(page);
      // Never resolve the route — the request hangs until the app's own
      // 10s AbortController fires, converting the fetch into an AbortError.
      await page.route(RESTORE_URL, () => new Promise(() => {}));
      await page.goto(`${env.frontendBase}/v2/helper/purchase`);
      await page.waitForSelector('[data-testid="restore-error-state"]', { timeout: 15000 });
      assert(await page.locator('[data-testid="restore-retry-btn"]').count() === 1, 'case 2: retry button missing after timeout');
      results.push('2: timeout/abort → error-state + Retry');
      await context.close();
    }

    // ── Case 3: HTTP 500 ──────────────────────────────────────────────────────
    {
      const context = await browser.newContext();
      const page = await context.newPage();
      await seedStorage(page);
      await page.route(RESTORE_URL, route => route.fulfill({
        status: 500,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'internal error', code: 'internal_error' }),
      }));
      await page.goto(`${env.frontendBase}/v2/helper/purchase`);
      await page.waitForSelector('[data-testid="restore-error-state"]', { timeout: 10000 });
      const errText = (await page.locator('.err').first().textContent().catch(() => '')).trim();
      assert(errText.length > 0, 'case 3: no visible error message on 500');
      assert(!errText.includes('internal error'), 'case 3: raw backend error text leaked into UI');
      assert(await page.locator('[data-testid="restore-retry-btn"]').count() === 1, 'case 3: retry button missing on 500');
      results.push('3: HTTP 500 → error-state + Retry, raw error hidden');
      await context.close();
    }

    // ── Case 4: HTTP 429 ──────────────────────────────────────────────────────
    {
      const context = await browser.newContext();
      const page = await context.newPage();
      await seedStorage(page);
      await page.route(RESTORE_URL, route => route.fulfill({
        status: 429,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'rate limit exceeded', code: 'rate_limited' }),
      }));
      await page.goto(`${env.frontendBase}/v2/helper/purchase`);
      await page.waitForSelector('[data-testid="restore-error-state"]', { timeout: 10000 });
      const errText = (await page.locator('.err').first().textContent().catch(() => '')).trim();
      assert(errText.length > 0, 'case 4: no visible error message on 429');
      assert(!errText.includes('rate limit'), 'case 4: raw backend error text leaked into UI on 429');
      assert(await page.locator('[data-testid="restore-retry-btn"]').count() === 1, 'case 4: retry button missing on 429');
      results.push('4: HTTP 429 → error-state + Retry, raw error hidden');
      await context.close();
    }

    // ── Case 5: malformed JSON body ───────────────────────────────────────────
    {
      const context = await browser.newContext();
      const page = await context.newPage();
      await seedStorage(page);
      await page.route(RESTORE_URL, route => route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: '{not valid json!!!',
      }));
      await page.goto(`${env.frontendBase}/v2/helper/purchase`);
      await page.waitForSelector('[data-testid="restore-error-state"]', { timeout: 10000 });
      const errText = (await page.locator('.err').first().textContent().catch(() => '')).trim();
      assert(errText.length > 0, 'case 5: no visible error message on malformed JSON');
      assert(!errText.includes('Unexpected'), 'case 5: raw JSON parse error leaked into UI');
      assert(await page.locator('[data-testid="restore-retry-btn"]').count() === 1, 'case 5: retry button missing on malformed JSON');
      results.push('5: malformed JSON → error-state + Retry, no raw parse error');
      await context.close();
    }

    // ── Case 6: HTTP 404 code=purchase_not_found → no_purchase, no Retry, storage cleared ──
    {
      const context = await browser.newContext();
      const page = await context.newPage();
      await seedStorage(page);
      await page.route(RESTORE_URL, route => route.fulfill({
        status: 404,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'purchase not found', code: 'purchase_not_found' }),
      }));
      await page.goto(`${env.frontendBase}/v2/helper/purchase`);
      await page.waitForSelector('.step-inner h2', { timeout: 10000 });
      assert(await page.locator('[data-testid="restore-error-state"]').count() === 0,
        'case 6: purchase_not_found incorrectly rendered the transient error-state branch');
      assert(await page.locator('[data-testid="restore-retry-btn"]').count() === 0,
        'case 6: Retry button must not be shown for purchase_not_found');
      const errText = (await page.locator('.err').first().textContent().catch(() => '')).trim();
      assert(errText.length > 0, 'case 6: no_purchase message not shown');
      const snap = await storageSnapshot(page);
      assert(snap.sessionToken === null && snap.activeHpt === null,
        'case 6: stale continuity state was not cleared for purchase_not_found');
      results.push('6: HTTP 404 purchase_not_found → no_purchase, no Retry, storage cleared');
      await context.close();
    }

    // ── Case 7: other 4xx (403) → safe generic message, no Retry ─────────────
    {
      const context = await browser.newContext();
      const page = await context.newPage();
      await seedStorage(page);
      await page.route(RESTORE_URL, route => route.fulfill({
        status: 403,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'super secret internal detail', code: 'forbidden_xyz' }),
      }));
      await page.goto(`${env.frontendBase}/v2/helper/purchase`);
      await page.waitForSelector('[data-testid="restore-error-state"]', { timeout: 10000 });
      const errText = (await page.locator('.err').first().textContent().catch(() => '')).trim();
      assert(errText.length > 0, 'case 7: no visible message on other 4xx');
      assert(!errText.includes('super secret internal detail'), 'case 7: raw backend body leaked into UI on other 4xx');
      assert(await page.locator('[data-testid="restore-retry-btn"]').count() === 0,
        'case 7: Retry must not be shown for a non-retryable other-4xx');
      results.push('7: other 4xx (403) → safe generic message, raw body hidden, no Retry');
      await context.close();
    }

    // ── Case 8: Retry after transient failure resumes SAME purchase, no create-invoice call ──
    {
      const context = await browser.newContext();
      const page = await context.newPage();
      await seedStorage(page);
      let createCalls = 0;
      await page.route(CREATE_URL, route => { createCalls++; return route.continue(); });
      let restoreCallCount = 0;
      let sawFailureThenSuccess = false;
      await page.route(RESTORE_URL, route => {
        restoreCallCount++;
        if (restoreCallCount === 1) return route.abort('failed');
        sawFailureThenSuccess = true;
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            phase: 'awaiting_payment',
            purchase_id: 'mock-purchase-id-1',
            currency: 'BTC',
            invoice: { payment_address: 'bc1qmockaddressxxxxxxxxxxxxxxxxxxxxxxxxxxx', amount_atomic: 100000 },
            provider_status: 'healthy',
          }),
        });
      });
      await page.goto(`${env.frontendBase}/v2/helper/purchase`);
      await page.waitForSelector('[data-testid="restore-error-state"]', { timeout: 10000 });
      await page.click('[data-testid="restore-retry-btn"]');
      await page.waitForSelector('.invoice-wrap', { timeout: 10000 });
      assert(sawFailureThenSuccess, 'case 8: retry did not re-call restore');
      assert(restoreCallCount === 2, `case 8: expected exactly 2 restore calls (1 fail + 1 retry), got ${restoreCallCount}`);
      assert(createCalls === 0, `case 8: retry must never call the create-invoice endpoint, got ${createCalls} call(s)`);
      const shownAddr = (await page.locator('.inv-val.addr, .invoice-shell').first().textContent().catch(() => ''));
      assert(shownAddr.includes('bc1qmockaddress'), 'case 8: retry did not restore the same mocked purchase/invoice');
      results.push('8: Retry resumes SAME purchase via restore endpoint, never calls create-invoice');
      await context.close();
    }

    // ── Case 9: terminal successful response still goes through existing routeByPhase ──
    {
      const context = await browser.newContext();
      const page = await context.newPage();
      await seedStorage(page);
      await page.route(RESTORE_URL, route => route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ phase: 'receipt_expired', purchase_id: 'mock-purchase-id-1', currency: 'BTC' }),
      }));
      await page.goto(`${env.frontendBase}/v2/helper/purchase`);
      await page.waitForFunction(() => {
        try { return localStorage.getItem('v2_active_hpt') === null; } catch { return false; }
      }, { timeout: 10000 });
      const snap = await storageSnapshot(page);
      assert(snap.sessionToken === null, 'case 9: terminal receipt_expired phase did not clear sessionStorage token (existing contract regressed)');
      assert(await page.locator('[data-testid="restore-error-state"]').count() === 0,
        'case 9: terminal phase incorrectly rendered the transient error-state branch');
      results.push('9: terminal successful response (200 + phase) still uses existing routeByPhase (untouched)');
      await context.close();
    }

    // ── Case 10: Listing/Board links present and correct on the error-state ──
    {
      const context = await browser.newContext();
      const page = await context.newPage();
      await seedStorage(page);
      await page.route(RESTORE_URL, route => route.abort('failed'));
      await page.goto(`${env.frontendBase}/v2/helper/purchase`);
      await page.waitForSelector('[data-testid="restore-error-state"]', { timeout: 10000 });
      const listingHref = await page.locator('a.back-link').first().getAttribute('href');
      const boardHref = await page.locator('a.back-link').nth(1).getAttribute('href');
      assert(listingHref === `/v2/listing/${LISTING_ID}`, `case 10: back-to-listing href wrong: ${listingHref}`);
      assert(boardHref === `/v2/board/${CITY}`, `case 10: back-to-board href wrong: ${boardHref}`);
      await page.click('a.back-link:nth-child(2)').catch(() => {});
      await page.waitForLoadState('networkidle');
      assert(page.url().includes(`/v2/board/${CITY}`), `case 10: clicking back-to-board did not navigate there, url=${page.url()}`);
      results.push('10: Listing/Board links present, correct, and navigable from the error-state');
      await context.close();
    }

    console.log(`\n  Run ${runNumber}: ${results.length}/10 focused cases passed`);
    for (const r of results) console.log(`    ✓ ${r}`);
    return { pass: results.length === 10 };
  } finally {
    await browser.close();
    await teardown(env);
  }
}

(async () => {
  let allPass = true;
  for (const run of [1, 2]) {
    console.log(`\n  ── Focused Restore-Error Run ${run} ──────────────────`);
    try {
      const { pass } = await runOnce(run);
      allPass = allPass && pass;
    } catch (e) {
      allPass = false;
      console.error(`  ✗ Run ${run} FAILED: ${e.message}`);
    }
  }
  console.log(`\n${'═'.repeat(56)}`);
  console.log(allPass ? '  RESULT: PASSED ✓' : '  RESULT: FAILED ✗');
  process.exit(allPass ? 0 : 1);
})();
