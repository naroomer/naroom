// 046_browser_principal.js — Playwright browser-level principal contract test
//
// Proves that the frontend honours the principal contract in the browser:
//   BP-1: /new  — recovery code shown BEFORE /wallet/register; after ack,
//          /wallet/register fires with Authorization: Bearer
//   BP-2: /helper — same ordering guarantee
//   BP-3: /new (revisit with stored token) — /session/status validates the
//          stored token; /session/init is NOT called; no recovery gate shown
//   BP-4: /resume — /session/recover token stored only under
//          naroom_session_{role}, not under both keys
//   BP-5: chat page re-auth screen asks for recovery code, not wallet address
//
// NOT included in selftest.sh (same pattern as 043_browser_renewal.js).
// Run directly: node e2e/tests/046_browser_principal.js
//
// Prerequisites:
//   cd e2e && npm i -D playwright
//   npx playwright install chromium

import { chromium }           from 'playwright';
import { spawn, execFileSync } from 'child_process';
import { mkdtempSync, rmSync } from 'fs';
import { tmpdir }              from 'os';
import { join }                from 'path';
import net                     from 'net';

// ── Constants ──────────────────────────────────────────────────────────────────
const BACKEND_DIR  = new URL('../../', import.meta.url).pathname.replace(/\/$/, '');
const FRONTEND_DIR = join(BACKEND_DIR, 'frontend');
const VITE_BIN     = join(FRONTEND_DIR, 'node_modules/.bin/vite');

const TEST_SALT    = 'e2e-browser-principal-salt';
const TEST_ENC_KEY = 'e2e-browser-principal-enc32!';
const WALLET_A     = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';

// ── Infra helpers ──────────────────────────────────────────────────────────────
const sleep = (ms) => new Promise(r => setTimeout(r, ms));

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
  throw new Error(`Port ${port} did not become ready within ${timeout}ms`);
}

async function waitPortClosed(port, timeout = 6000) {
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

async function killGroup(proc, label, timeoutMs = 5000) {
  if (!proc || proc.exitCode !== null || proc.pid == null) return;
  const pid = proc.pid;
  return new Promise(resolve => {
    const kTimer = setTimeout(() => {
      if (proc.exitCode !== null) { resolve(); return; }
      try { process.kill(-pid, 'SIGKILL'); } catch {}
    }, timeoutMs);
    proc.once('exit', () => { clearTimeout(kTimer); resolve(); });
    try { process.kill(-pid, 'SIGTERM'); } catch { clearTimeout(kTimer); resolve(); }
  });
}

// ── Logging ────────────────────────────────────────────────────────────────────
const pass = (msg) => console.log(`  ✓ ${msg}`);
const fail = (msg) => { console.error(`  ✗ ${msg}`); throw new Error(msg); };

// Attach a request collector to a page; returns the live array.
function trackApiRequests(page) {
  const reqs = [];
  page.on('request', req => {
    const url = req.url();
    if (url.includes('/api/')) {
      reqs.push({ url, method: req.method(), headers: req.headers() });
    }
  });
  return reqs;
}

// Fill the /new form: click first dep, first help type, urgency=soon (index 1),
// first language, and type the wallet address.
// Waits for the form to be fully rendered before interacting.
async function fillNewForm(page, wallet) {
  // Click option buttons (caller must ensure Svelte is hydrated before calling)
  await page.locator('button.opt').filter({ hasText: /^Alcohol$/ }).click();        // dependency
  await page.locator('button.opt').filter({ hasText: /^Crisis support$/ }).click(); // help type
  await page.locator('button.opt').filter({ hasText: /^Soon$/ }).click();           // urgency
  // Language — click the first opt inside the language field (avoids lang-switcher)
  await page.locator('.field').nth(4).locator('button.opt').first().click();        // EN

  // Fill wallet address
  await page.locator('input[placeholder]').last().fill(wallet);
  await sleep(300); // Svelte reactivity tick
}

// ── Main ───────────────────────────────────────────────────────────────────────
async function run() {
  console.log('\n=== 046: Browser-Level Principal Contract Test (Playwright) ===\n');

  const tmpDir     = mkdtempSync(join(tmpdir(), 'naroom-bp-'));
  const dbPath     = join(tmpDir, 'naroom.db');
  const binaryPath = join(tmpDir, 'naroom-bp');

  const [BACKEND_PORT, FRONTEND_PORT] = await getFreePorts(2);
  const BACKEND_URL  = `http://127.0.0.1:${BACKEND_PORT}`;
  const FRONTEND_URL = `http://127.0.0.1:${FRONTEND_PORT}`;

  console.log(`  Ports  backend=${BACKEND_PORT}  frontend=${FRONTEND_PORT}`);

  // ── Build backend binary ──────────────────────────────────────────────────────
  console.log('  Building backend binary…');
  execFileSync(
    'go', ['build', '-tags', 'dev', '-o', binaryPath, './cmd/naroom/main.go'],
    { cwd: BACKEND_DIR, stdio: ['ignore', 'inherit', 'inherit'] },
  );

  // ── Spawn backend ─────────────────────────────────────────────────────────────
  const backend = spawn(binaryPath, [], {
    env: {
      ...process.env,
      DEV_MODE: 'true', SERVER_SALT: TEST_SALT, HASH_KEY: TEST_SALT,
      WALLET_ENC_KEY: TEST_ENC_KEY, PORT: String(BACKEND_PORT),
      DB_PATH: dbPath, TTL_CLEAN_INTERVAL: '5', INVOICE_WATCH_INTERVAL: '2',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
    detached: true,
  });
  backend.stdout.on('data', () => {});
  backend.stderr.on('data', () => {});

  // ── Spawn Vite dev server ─────────────────────────────────────────────────────
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
  vite.stdout.on('data', () => {});
  vite.stderr.on('data', d => {
    const s = d.toString();
    if (s.includes('Error') || s.includes('500'))
      process.stderr.write('[vite] ' + s);
  });

  // ── Cleanup ───────────────────────────────────────────────────────────────────
  let cleanupDone = false;
  async function cleanup() {
    if (cleanupDone) return;
    cleanupDone = true;
    await Promise.all([killGroup(backend, 'backend'), killGroup(vite, 'vite')]);
    await sleep(400);
    try { rmSync(tmpDir, { recursive: true, force: true }); } catch {}
    const bc = await waitPortClosed(BACKEND_PORT);
    const fc = await waitPortClosed(FRONTEND_PORT);
    if (!bc) console.error(`  [cleanup] WARNING: port ${BACKEND_PORT} still open`);
    if (!fc) console.error(`  [cleanup] WARNING: port ${FRONTEND_PORT} still open`);
  }

  process.on('exit', () => {
    if (backend.exitCode === null && backend.pid) try { process.kill(-backend.pid, 'SIGKILL'); } catch {}
    if (vite.exitCode === null    && vite.pid)    try { process.kill(-vite.pid,    'SIGKILL'); } catch {}
  });
  process.on('SIGINT',  () => cleanup().then(() => process.exit(130)));
  process.on('SIGTERM', () => cleanup().then(() => process.exit(130)));

  let browser;
  let passed = 0;
  let failed = 0;

  const check = (label, fn) =>
    fn()
      .then(() => { pass(label); passed++; })
      .catch(e  => { console.error(`  ✗ ${label} — ${e.message}`); failed++; });

  try {
    await Promise.all([
      waitForPort(BACKEND_PORT,  25000),
      waitForPort(FRONTEND_PORT, 40000),
    ]);
    console.log('  Both servers ready.\n');

    browser = await chromium.launch({ headless: true });

    // ── BP-1: /new — recovery gate BEFORE /wallet/register ───────────────────
    console.log('[BP-1] /new: recovery gate fires before /wallet/register');
    {
      const ctx  = await browser.newContext();
      const page = await ctx.newPage();
      const reqs = trackApiRequests(page);

      await page.goto(`${FRONTEND_URL}/new`, { waitUntil: 'load' });
      await sleep(1500); // wait for Svelte hydration
      await fillNewForm(page, WALLET_A);

      // Submit the form
      await page.locator('button.submit:not([disabled])').click({ timeout: 10000 });

      // Wait for recovery code screen to appear
      await page.waitForSelector('h2', { timeout: 10000 });
      const h2Text = await page.locator('h2').textContent().catch(() => '');
      if (!h2Text.toLowerCase().includes('recovery')) {
        const body = await page.evaluate(() => document.body.innerText).catch(() => '');
        fail(`BP-1: expected recovery heading, got "${h2Text}" — body: ${body.slice(0,200)}`);
      }

      // At this point /wallet/register must NOT have been called
      const walletRegsBefore = reqs.filter(r => r.url.includes('/wallet/register'));
      if (walletRegsBefore.length > 0)
        fail(`BP-1: /wallet/register called before recovery ack (${walletRegsBefore.length} call(s) found)`);
      pass('BP-1a: recovery screen visible; /wallet/register not called yet');

      // /session/init must have been called to create the session
      const sessionInits = reqs.filter(r => r.url.includes('/session/init'));
      if (sessionInits.length === 0)
        fail('BP-1: /session/init was not called before recovery screen');
      pass('BP-1b: /session/init called before recovery screen');
      passed += 2;

      // Click "I saved it — continue →"
      await page.locator('button.submit').filter({ hasText: /saved/i }).click();
      await sleep(2000); // let /wallet/register fire

      // Now /wallet/register must appear with Authorization: Bearer
      const walletRegsAfter = reqs.filter(r => r.url.includes('/wallet/register'));
      if (walletRegsAfter.length === 0)
        fail('BP-1: /wallet/register not called after recovery ack');
      const auth1 = walletRegsAfter[0].headers['authorization'] ?? '';
      if (!auth1.startsWith('Bearer '))
        fail(`BP-1: /wallet/register missing Authorization Bearer (got: "${auth1}")`);
      pass('BP-1c: /wallet/register called after ack with Authorization: Bearer');
      passed++;

      await ctx.close();
    }

    // ── BP-2: /helper — recovery gate BEFORE /wallet/register ────────────────
    console.log('\n[BP-2] /helper: recovery gate fires before /wallet/register');
    {
      const ctx  = await browser.newContext();
      const page = await ctx.newPage();
      const reqs = trackApiRequests(page);

      await page.goto(`${FRONTEND_URL}/helper`, { waitUntil: 'load' });
      await sleep(1500); // wait for Svelte hydration
      // Fill wallet input (the monospace .input field)
      await page.locator('input.input').fill(WALLET_A);
      await sleep(200);

      // Click subscribe
      await page.locator('button.submit:not([disabled])').click({ timeout: 10000 });

      // Wait for recovery code screen
      await page.waitForSelector('h2', { timeout: 10000 });
      const h2Text = await page.locator('h2').textContent().catch(() => '');
      if (!h2Text.toLowerCase().includes('recovery'))
        fail(`BP-2: expected recovery heading, got "${h2Text}"`);

      const walletRegsBefore = reqs.filter(r => r.url.includes('/wallet/register'));
      if (walletRegsBefore.length > 0)
        fail(`BP-2: /wallet/register called before recovery ack (${walletRegsBefore.length} call(s))`);
      pass('BP-2a: recovery screen visible; /wallet/register not called yet');

      const sessionInits = reqs.filter(r => r.url.includes('/session/init'));
      if (sessionInits.length === 0)
        fail('BP-2: /session/init was not called before recovery screen');
      pass('BP-2b: /session/init called before recovery screen');
      passed += 2;

      await page.locator('button.submit').filter({ hasText: /saved/i }).click();
      await sleep(2000);

      const walletRegsAfter = reqs.filter(r => r.url.includes('/wallet/register'));
      if (walletRegsAfter.length === 0)
        fail('BP-2: /wallet/register not called after recovery ack');
      const auth2 = walletRegsAfter[0].headers['authorization'] ?? '';
      if (!auth2.startsWith('Bearer '))
        fail(`BP-2: /wallet/register missing Authorization Bearer (got: "${auth2}")`);
      pass('BP-2c: /wallet/register called after ack with Authorization: Bearer');
      passed++;

      await ctx.close();
    }

    // ── BP-3: /new revisit — existing token validated via /session/status ─────
    console.log('\n[BP-3] /new with stored session: /session/status validates; no recovery gate');
    {
      // Create a valid session via backend API
      const initRes = await fetch(`${BACKEND_URL}/session/init`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ role: 'client' }),
      });
      if (!initRes.ok) fail(`BP-3: /session/init returned ${initRes.status}`);
      const initData = await initRes.json();
      const validToken = initData.session_token;

      // Pre-seed sessionStorage with the valid token before page loads
      const ctx = await browser.newContext();
      await ctx.addInitScript((tok) => {
        sessionStorage.setItem('naroom_session_client', tok);
      }, validToken);

      const page = await ctx.newPage();
      const reqs = trackApiRequests(page);

      await page.goto(`${FRONTEND_URL}/new`, { waitUntil: 'load' });
      await sleep(1500); // wait for Svelte hydration
      await fillNewForm(page, WALLET_A);

      await page.locator('button.submit:not([disabled])').click({ timeout: 10000 });
      await sleep(2000);

      // /session/status must have been called to validate the stored token
      const statusCalls = reqs.filter(r => r.url.includes('/session/status'));
      if (statusCalls.length === 0)
        fail('BP-3: /session/status not called to validate stored token');
      pass('BP-3a: /session/status called to validate stored token');
      passed++;

      // /session/init must NOT have been called (existing session is valid)
      const initCalls = reqs.filter(r => r.url.includes('/session/init'));
      if (initCalls.length > 0)
        fail(`BP-3: /session/init called despite valid stored session (${initCalls.length} time(s))`);
      pass('BP-3b: /session/init not called (existing session valid)');
      passed++;

      // No recovery gate
      const recoveryVisible = await page.locator('h2').filter({ hasText: /recovery/i }).isVisible().catch(() => false);
      if (recoveryVisible)
        fail('BP-3: recovery gate shown for user with existing valid session');
      pass('BP-3c: no recovery gate for existing valid session');
      passed++;

      // /wallet/register must have been called with Bearer
      const walletRegs = reqs.filter(r => r.url.includes('/wallet/register'));
      if (walletRegs.length === 0)
        fail('BP-3: /wallet/register not called');
      const auth3 = walletRegs[0].headers['authorization'] ?? '';
      if (!auth3.startsWith('Bearer '))
        fail(`BP-3: /wallet/register missing Bearer (got: "${auth3}")`);
      pass('BP-3d: /wallet/register called with Authorization: Bearer');
      passed++;

      await ctx.close();
    }

    // ── BP-4: /resume — token stored only under naroom_session_{role} ─────────
    console.log('\n[BP-4] /resume: recovered token stored under role key only (not both)');
    {
      // Create a session with a known recovery_code (role=peer)
      const initRes = await fetch(`${BACKEND_URL}/session/init`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ role: 'peer' }),
      });
      if (!initRes.ok) fail(`BP-4: /session/init returned ${initRes.status}`);
      const initData = await initRes.json();
      if (!initData.recovery_code) fail('BP-4: /session/init did not return recovery_code');
      const recoveryCode = initData.recovery_code;

      const ctx  = await browser.newContext(); // empty sessionStorage
      const page = await ctx.newPage();

      await page.goto(`${FRONTEND_URL}/resume`, { waitUntil: 'load' });
      await sleep(2000); // wait for Svelte hydration + onMount auto-check (finds nothing, shows form)

      const input = page.locator('input[placeholder*="recovery"]');
      const inputVisible = await input.isVisible().catch(() => false);
      if (!inputVisible)
        fail('BP-4: recovery code input not visible on /resume');

      await input.fill(recoveryCode);
      await page.locator('button:not([disabled])').filter({ hasText: /restore/i }).click();
      await sleep(1500);

      // New recovery code display should appear
      const newCodeBox = page.locator('.recovery-box');
      const newCodeVisible = await newCodeBox.isVisible().catch(() => false);
      if (!newCodeVisible)
        fail('BP-4: new recovery code box not shown after /session/recover');
      pass('BP-4a: new recovery code box shown after /session/recover');
      passed++;

      // Ack the new code
      await page.locator('button').filter({ hasText: /saved/i }).click();
      await sleep(800);

      // Read sessionStorage in browser
      const storage = await page.evaluate(() => ({
        peer:   sessionStorage.getItem('naroom_session_peer'),
        client: sessionStorage.getItem('naroom_session_client'),
      }));

      if (!storage.peer)
        fail('BP-4: naroom_session_peer not set after peer recovery');
      pass('BP-4b: naroom_session_peer set');
      passed++;

      if (storage.client !== null)
        fail(`BP-4: naroom_session_client was also set (must be null for peer — got: "${String(storage.client).slice(0,20)}...")`);
      pass('BP-4c: naroom_session_client NOT set (role=peer → only peer key written)');
      passed++;

      await ctx.close();
    }

    // ── BP-5: chat re-auth asks for recovery code, not wallet address ─────────
    console.log('\n[BP-5] chat page: re-auth asks for recovery code (not wallet address)');
    {
      // Navigate without any session → loadRoom() sends request without auth → 401 → needsAuth=true
      const ctx  = await browser.newContext(); // empty sessionStorage
      const page = await ctx.newPage();

      await page.goto(`${FRONTEND_URL}/chat/bp5_fake_room`, { waitUntil: 'load' });
      await sleep(2000); // Svelte hydration + 401 detection

      // Should show re-auth screen (needsAuth=true after 401)
      const input = page.locator('input[placeholder]').first();
      const inputVisible = await input.isVisible().catch(() => false);
      if (!inputVisible) {
        const body = await page.evaluate(() => document.body.innerText).catch(() => '');
        fail(`BP-5: re-auth input not visible (page: "${body.slice(0, 200)}")`);
      }

      const placeholder = await input.getAttribute('placeholder') ?? '';
      // Must NOT mention wallet/BTC/LTC
      if (/btc|ltc|wallet|address/i.test(placeholder))
        fail(`BP-5: re-auth input asks for wallet address (placeholder: "${placeholder}")`);
      pass(`BP-5a: re-auth input does not ask for wallet (placeholder: "${placeholder}")`);
      passed++;

      // Must mention recovery code
      if (!(/recovery/i.test(placeholder))) {
        // The placeholder says "Paste your recovery code..." — check the surrounding text
        const text = await page.evaluate(() => document.body.innerText).catch(() => '');
        if (!/recovery/i.test(text))
          fail(`BP-5: chat re-auth does not mention recovery code (body: "${text.slice(0,300)}")`);
      }
      pass('BP-5b: chat re-auth mentions recovery code');
      passed++;

      await ctx.close();
    }

  } finally {
    if (browser) await browser.close().catch(() => {});
    await cleanup();
  }

  const total = passed + failed;
  console.log(`\n  046_browser_principal: ${passed}/${total} passed`);
  return failed === 0;
}

run()
  .then(ok => process.exit(ok ? 0 : 1))
  .catch(e  => { console.error('  FATAL:', e.message); process.exit(1); });
