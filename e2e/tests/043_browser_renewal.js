// 043_browser_renewal.js — browser-level renewal test (Playwright)
//
// This is a BROWSER-LEVEL test. It exercises the actual SvelteKit frontend in a real
// Chromium browser, including wallet authentication, the free-renewal UI flow, board
// appearance, duplicate-renewal blocking, and wrong-wallet access control.
//
// NOT included in selftest.sh (same pattern as 026_analytics_privacy.js).
//
// ── Prerequisites ──────────────────────────────────────────────────────────────
//   cd e2e && npm i -D playwright
//   npx playwright install chromium
//
// ── How to run ─────────────────────────────────────────────────────────────────
//   node e2e/tests/043_browser_renewal.js
//
// Ports are allocated dynamically — no fixed port requirements.
// The test builds a temporary Go binary and launches the Vite dev server directly.
//
// ── What is tested ─────────────────────────────────────────────────────────────
//   BT1: expired listing page shows owner wallet authentication form
//   BT2: wrong wallet cannot unlock renewal — visible error, renew section absent
//   BT3: correct owner wallet → "View Responses" → renew button becomes visible
//   BT4: clicking the renew button shows visible success message, then page reloads to active state
//   BT5: renewed listing card appears in the rendered board DOM (href match)
//   BT6: listing page after renewal (active, >1h left) does not show renew button

import { chromium }                     from 'playwright';
import { spawn, execFileSync }          from 'child_process';
import { mkdtempSync, rmSync }          from 'fs';
import { tmpdir }                       from 'os';
import { join }                         from 'path';
import net                              from 'net';

// ── Constants ──────────────────────────────────────────────────────────────────
const BACKEND_DIR  = new URL('../../', import.meta.url).pathname.replace(/\/$/, '');
const FRONTEND_DIR = join(BACKEND_DIR, 'frontend');
const VITE_BIN     = join(FRONTEND_DIR, 'node_modules/.bin/vite');

const TEST_SALT     = 'e2e-test-salt';
const TEST_ENC_KEY  = 'e2e-test-wallet-enc-key-32bytes!';
const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf'; // listing owner
const WRONG_WALLET  = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT'; // different person

// ── Helpers ────────────────────────────────────────────────────────────────────
const sleep = (ms) => new Promise(r => setTimeout(r, ms));

// Reserve N sockets simultaneously so all returned ports are guaranteed distinct.
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

// Poll until a TCP connection to 127.0.0.1:port succeeds.
async function waitForPort(port, timeout = 40000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    const ok = await new Promise(resolve => {
      const c = net.createConnection(port, '127.0.0.1');
      c.on('connect', () => { c.destroy(); resolve(true); });
      c.on('error', () => resolve(false));
    });
    if (ok) return;
    await sleep(300);
  }
  throw new Error(`Port ${port} did not become ready within ${timeout}ms`);
}

// Poll until no TCP listener on 127.0.0.1:port; returns true when closed.
async function waitPortClosed(port, timeout = 6000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    const open = await new Promise(resolve => {
      const c = net.createConnection(port, '127.0.0.1');
      c.on('connect', () => { c.destroy(); resolve(true); });
      c.on('error', () => resolve(false));
    });
    if (!open) return true;
    await sleep(200);
  }
  return false;
}

// Send SIGTERM to a process group; SIGKILL after timeout if still alive.
// Resolves when the process has exited.
async function killGroup(proc, label, timeoutMs = 5000) {
  if (!proc || proc.exitCode !== null || proc.pid == null) return;
  const pid = proc.pid;
  console.log(`  [cleanup] SIGTERM pgid=${pid} (${label})`);
  return new Promise(resolve => {
    const kill = setTimeout(() => {
      if (proc.exitCode !== null) { resolve(); return; }
      console.log(`  [cleanup] SIGKILL pgid=${pid} (${label}) after ${timeoutMs}ms`);
      try { process.kill(-pid, 'SIGKILL'); } catch {}
    }, timeoutMs);
    proc.once('exit', () => { clearTimeout(kill); resolve(); });
    try { process.kill(-pid, 'SIGTERM'); } catch {
      clearTimeout(kill);
      resolve();
    }
  });
}

function dbExec(dbPath, sql) {
  return execFileSync('sqlite3', [dbPath, sql], { encoding: 'utf8' }).trim();
}

// ── Logging ────────────────────────────────────────────────────────────────────
let stepN = 0;
const pass = (msg) => console.log(`  ✓ ${msg}`);

// fail() prints the message and throws so the exception propagates through
// try/finally, which always runs cleanup before the process exits.
const fail = (msg) => {
  console.error(`  ✗ ${msg}`);
  throw new Error(msg);
};

const step = (msg) => console.log(`\n[BT${++stepN}] ${msg}`);

// ── Main ───────────────────────────────────────────────────────────────────────
async function run() {
  console.log('\n=== 043: Browser-Level Renewal Test (Playwright) ===\n');

  // ── Temp dir and simultaneously-allocated distinct ports ────────────────────
  const tmpDir     = mkdtempSync(join(tmpdir(), 'naroom-browser-e2e-'));
  const dbPath     = join(tmpDir, 'naroom.db');
  const binaryPath = join(tmpDir, 'naroom-e2e');

  const [BACKEND_PORT, FRONTEND_PORT] = await getFreePorts(2);
  const BACKEND_URL  = `http://127.0.0.1:${BACKEND_PORT}`;
  const FRONTEND_URL = `http://127.0.0.1:${FRONTEND_PORT}`;

  console.log(`Ports  backend=${BACKEND_PORT}  frontend=${FRONTEND_PORT}`);

  // ── API helpers (close over BACKEND_URL) ────────────────────────────────────
  async function apiPost(path, body = {}, wallet = CLIENT_WALLET) {
    const res = await fetch(`${BACKEND_URL}${path}`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Dev-Wallet': wallet,
        'X-Dev-Role': 'client',
      },
      body: JSON.stringify(body),
    });
    return { status: res.status, body: await res.json().catch(() => ({})) };
  }

  async function apiGet(path, wallet = CLIENT_WALLET) {
    const res = await fetch(`${BACKEND_URL}${path}`, {
      headers: { 'X-Dev-Wallet': wallet, 'X-Dev-Role': 'client' },
    });
    return { status: res.status, body: await res.json().catch(() => ({})) };
  }

  // ── Build backend binary ─────────────────────────────────────────────────────
  // Using a pre-built binary (not go run) gives a single PID with full lifecycle
  // control; no compiler sub-processes are left behind.
  console.log('Building backend binary…');
  execFileSync(
    'go', ['build', '-tags', 'dev', '-o', binaryPath, './cmd/naroom/main.go'],
    { cwd: BACKEND_DIR, stdio: ['ignore', 'inherit', 'inherit'] },
  );
  console.log(`Binary: ${binaryPath}`);

  // ── Spawn backend (detached → own process group) ─────────────────────────────
  console.log(`Starting backend on port ${BACKEND_PORT}…`);
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
    },
    stdio: ['ignore', 'pipe', 'pipe'],
    detached: true,
  });
  backend.stdout.on('data', () => {});
  backend.stderr.on('data', () => {});

  // ── Spawn Vite directly (not via npm) → single node PID, no wrapper ─────────
  // BACKEND_URL is forwarded so both the SSR load functions (+page.server.js) and
  // the Vite proxy rewrite target use the dynamically chosen backend port.
  console.log(`Starting Vite dev server on port ${FRONTEND_PORT}…`);
  const vite = spawn(
    VITE_BIN,
    ['dev', '--port', String(FRONTEND_PORT), '--host', '127.0.0.1', '--strictPort'],
    {
      cwd: FRONTEND_DIR,
      env: { ...process.env, BACKEND_URL, NO_COLOR: '1' },
      stdio: ['ignore', 'pipe', 'pipe'],
      detached: true,
    },
  );
  vite.stdout.on('data', d => {
    const s = d.toString();
    if (s.includes('error') || s.includes('Error') || s.includes('500'))
      process.stderr.write('[vite stdout] ' + s);
  });
  vite.stderr.on('data', d => {
    const s = d.toString();
    if (s.includes('error') || s.includes('Error'))
      process.stderr.write('[vite stderr] ' + s);
  });

  // ── Cleanup ──────────────────────────────────────────────────────────────────
  // Called from: finally (success/assertion-error/exception) AND signal handlers.
  let cleanupDone = false;
  async function cleanup() {
    if (cleanupDone) return { backendClosed: true, frontendClosed: true };
    cleanupDone = true;
    await Promise.all([
      killGroup(backend, 'backend'),
      killGroup(vite, 'vite'),
    ]);
    await sleep(400);
    try { rmSync(tmpDir, { recursive: true, force: true }); } catch {}
    const backendClosed  = await waitPortClosed(BACKEND_PORT);
    const frontendClosed = await waitPortClosed(FRONTEND_PORT);
    console.log(
      `  [cleanup] port ${BACKEND_PORT} closed=${backendClosed}` +
      `  port ${FRONTEND_PORT} closed=${frontendClosed}`,
    );
    if (!backendClosed)
      console.error(`  [cleanup] WARNING: port ${BACKEND_PORT} still open after cleanup`);
    if (!frontendClosed)
      console.error(`  [cleanup] WARNING: port ${FRONTEND_PORT} still open after cleanup`);
    return { backendClosed, frontendClosed };
  }

  // Synchronous last-resort SIGKILL on unexpected Node exit.
  process.on('exit', () => {
    if (backend.exitCode === null && backend.pid) {
      try { process.kill(-backend.pid, 'SIGKILL'); } catch {}
    }
    if (vite.exitCode === null && vite.pid) {
      try { process.kill(-vite.pid, 'SIGKILL'); } catch {}
    }
  });

  let signalCleanupDone = false;
  async function handleSignal(sig) {
    if (signalCleanupDone) return;
    signalCleanupDone = true;
    console.log(`\nCaught ${sig} — cleaning up…`);
    await cleanup();
    process.exit(130);
  }
  process.on('SIGINT',  () => handleSignal('SIGINT'));
  process.on('SIGTERM', () => handleSignal('SIGTERM'));

  let browser, listingId, cleanupResult;

  try {
    // ── Wait for both servers ─────────────────────────────────────────────────
    await Promise.all([
      waitForPort(BACKEND_PORT,  25000),
      waitForPort(FRONTEND_PORT, 40000),
    ]);
    console.log('Both servers ready.\n');

    // ── Seed via API ──────────────────────────────────────────────────────────
    await apiPost('/wallet/register', { wallet_address: CLIENT_WALLET, currency: 'BTC', role: 'client' });
    await apiPost('/wallet/register', { wallet_address: WRONG_WALLET,  currency: 'BTC', role: 'client' }, WRONG_WALLET);

    const cr = await apiPost('/listing/create', {
      city: 'new_york', dependency_type: 'alcohol', help_type: 'just_talk',
      urgency: 'soon', languages: ['en'],
    });
    if (cr.status !== 201) fail(`createListing returned ${cr.status}: ${JSON.stringify(cr.body)}`);
    listingId = cr.body.listing_id;
    console.log(`Listing created: ${listingId}`);

    for (let i = 0; i < 30; i++) {
      const lr = await apiGet(`/listing/${listingId}`);
      if (lr.body.status === 'active') break;
      await sleep(1000);
      if (i === 29) fail('Listing never became active after 30s');
    }

    dbExec(dbPath,
      `UPDATE listings SET first_activated_at = strftime('%s','now') - 60*86400, ` +
      `visible_until = strftime('%s','now') - 100, status = 'expired' WHERE id = '${listingId}'`,
    );

    const lrPre = await apiGet(`/listing/${listingId}`);
    if (lrPre.status !== 200)            fail(`pre-check: GET listing returned ${lrPre.status}`);
    if (lrPre.body.status !== 'expired') fail(`pre-check: expected expired, got ${lrPre.body.status}`);
    if (!lrPre.body.can_renew)           fail('pre-check: expected can_renew=true');
    console.log(`  pre-check: status=${lrPre.body.status} can_renew=${lrPre.body.can_renew}\n`);

    // ── Launch browser ────────────────────────────────────────────────────────
    browser = await chromium.launch({ headless: true });

    // ── BT1: Expired listing shows wallet auth form ───────────────────────────
    step('Expired listing page shows owner wallet authentication form');
    {
      const ctx  = await browser.newContext();
      const page = await ctx.newPage();
      await page.goto(`${FRONTEND_URL}/listing/${listingId}`, { waitUntil: 'networkidle' });
      await page.waitForTimeout(600);

      const walletInput = page.locator('#client-wallet');
      const visible = await walletInput.isVisible().catch(() => false);
      if (!visible) {
        const body = await page.evaluate(() => document.body.innerText).catch(() => '(error)');
        console.log('  [debug] page body:', body.slice(0, 400).replace(/\n/g, ' '));
        fail('Expected #client-wallet input to be visible on expired listing page');
      }
      pass('Owner wallet input (#client-wallet) visible on expired listing page');
      await ctx.close();
    }

    // ── BT2: Wrong wallet → visible error, renew section absent ──────────────
    step('Wrong wallet: visible error message, renew section absent');
    {
      const ctx  = await browser.newContext();
      const page = await ctx.newPage();
      await page.goto(`${FRONTEND_URL}/listing/${listingId}`, { waitUntil: 'domcontentloaded' });
      await page.waitForTimeout(400);

      await page.fill('#client-wallet', WRONG_WALLET);
      await page.locator('button.btn-primary', { hasText: /view|responses/i }).first().click();
      await page.waitForTimeout(2500);

      const errDiv = page.locator('.section .error');
      const errVisible = await errDiv.isVisible().catch(() => false);
      if (!errVisible) {
        const body = await page.evaluate(() => document.body.innerText).catch(() => '(error)');
        console.log('  [debug] page body:', body.slice(0, 500).replace(/\n/g, ' '));
        fail('Expected .section .error div to be visible for wrong wallet');
      }
      const errText = await errDiv.textContent().catch(() => '');
      pass(`Wrong wallet: error visible — "${errText.trim().slice(0, 80)}"`);

      const renewVisible = await page.locator('.renew-section').isVisible().catch(() => false);
      if (renewVisible) fail('Wrong wallet must not unlock .renew-section');
      pass('Wrong wallet: .renew-section absent');
      await ctx.close();
    }

    // ── BT3: Correct wallet → renew section + button visible ─────────────────
    step('Correct owner wallet → renew section and button visible');
    let page3, ctx3;
    {
      ctx3  = await browser.newContext();
      page3 = await ctx3.newPage();
      await page3.goto(`${FRONTEND_URL}/listing/${listingId}`, { waitUntil: 'domcontentloaded' });
      await page3.waitForTimeout(400);

      await page3.fill('#client-wallet', CLIENT_WALLET);
      await page3.locator('button.btn-primary', { hasText: /view|responses/i }).first().click();
      await page3.waitForTimeout(2500);

      const renewSection = page3.locator('.renew-section');
      const renewVisible = await renewSection.isVisible().catch(() => false);
      if (!renewVisible) {
        const body = await page3.evaluate(() => document.body.innerText).catch(() => '(error)');
        console.log('  [debug] page body:', body.slice(0, 500).replace(/\n/g, ' '));
        fail('.renew-section should be visible for correct owner wallet on expired listing');
      }
      pass('.renew-section visible for correct owner wallet');

      const freeRenewBtn = renewSection.locator('button.btn-primary', { hasText: /extend|renew|free/i });
      if (!await freeRenewBtn.isVisible().catch(() => false))
        fail('Free renewal button must be visible inside .renew-section');
      pass('Free renewal button visible inside .renew-section');
      // page3 / ctx3 kept open for BT4
    }

    // ── BT4: Click renew → success message → page reloads → active ───────────
    step('Click renew: success message visible before reload, then active state');
    {
      const renewSection = page3.locator('.renew-section');
      const freeRenewBtn = renewSection.locator('button.btn-primary', { hasText: /extend|renew|free/i });
      await freeRenewBtn.click();

      // Component sets renewDone=true right after the API call, 1500 ms before reload.
      const successPara = page3.locator('.renew-section p.section-desc', { hasText: /extended|24/i });
      try {
        await successPara.waitFor({ state: 'visible', timeout: 6000 });
        pass('Success paragraph ("Extended for 24 more hours") visible after clicking Renew');
      } catch {
        fail('Success paragraph did not appear within 6 s after clicking Renew');
      }

      // Wait for the reload timer + re-render
      await page3.waitForTimeout(3500);

      if (await page3.locator('.renew-section').isVisible().catch(() => false))
        fail('.renew-section must be gone after reload (listing now active)');
      pass('.renew-section gone after page reload');

      const lrAfter = await apiGet(`/listing/${listingId}`);
      if (lrAfter.body.status !== 'active')
        fail(`Expected status=active after renewal, got ${lrAfter.body.status}`);
      pass('Backend confirms listing status=active after renewal');

      await ctx3.close();
    }

    // ── BT5: Renewed listing card present in board DOM ────────────────────────
    step('Renewed listing card visible in rendered board DOM');
    {
      const ctx  = await browser.newContext();
      const page = await ctx.newPage();
      await page.goto(`${FRONTEND_URL}/board/new_york`, { waitUntil: 'networkidle' });
      await page.waitForTimeout(800);

      const listingCard = page.locator(`a.card.listing[href="/listing/${listingId}"]`);
      const cardVisible = await listingCard.isVisible().catch(() => false);
      if (!cardVisible) {
        const html = await page.evaluate(
          () => document.querySelector('.grid')?.innerHTML ?? '(no .grid)',
        ).catch(() => '');
        console.log('  [debug] .grid innerHTML (first 600):', html.slice(0, 600));
        fail(`a.card.listing[href="/listing/${listingId}"] not found in board DOM`);
      }
      pass(`Listing card a.card.listing[href="/listing/${listingId}"] visible in board DOM`);
      await ctx.close();
    }

    // ── BT6: Active listing with >1 h left → no renew section ────────────────
    step('Renew section absent when listing has >1 h remaining (duplicate prevention)');
    {
      const ctx  = await browser.newContext();
      const page = await ctx.newPage();
      await page.goto(`${FRONTEND_URL}/listing/${listingId}`, { waitUntil: 'domcontentloaded' });
      await page.waitForTimeout(400);

      // Active listing shows the wallet input in the CLIENT section.
      const walletInput = page.locator('#client-wallet');
      if (await walletInput.isVisible().catch(() => false)) {
        await page.fill('#client-wallet', CLIENT_WALLET);
        const viewBtn = page.locator('button.btn-primary', { hasText: /view|responses/i }).first();
        if (await viewBtn.isVisible().catch(() => false)) {
          await viewBtn.click();
          await page.waitForTimeout(2500);
        }
      }

      // showRenew requires time_left < 3600 OR status=expired — neither applies after renewal.
      if (await page.locator('.renew-section').isVisible().catch(() => false))
        fail('.renew-section must not appear when listing has >1 h left');
      pass('.renew-section absent for active listing with >1 h left (duplicate correctly blocked)');
      await ctx.close();
    }

  } finally {
    // Cleanup always runs: success, assertion failure (thrown by fail()), or exception.
    if (browser) await browser.close().catch(() => {});
    cleanupResult = await cleanup();
  }

  // ── Final port-closure check (outside try/finally — cleanup already done) ───
  console.log('\n✓ All browser renewal tests passed (6/6)');
  console.log(`  backend port  ${BACKEND_PORT}: closed=${cleanupResult.backendClosed}`);
  console.log(`  frontend port ${FRONTEND_PORT}: closed=${cleanupResult.frontendClosed}`);
  if (!cleanupResult.backendClosed || !cleanupResult.frontendClosed) {
    throw new Error('CLEANUP FAILURE: at least one port is still open');
  }
  console.log('');
}

// Cleanup has already run inside run() before this catch fires.
run().catch(e => {
  console.error('\nBrowser test failed:', e.message);
  process.exit(1);
});
