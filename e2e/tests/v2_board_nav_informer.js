#!/usr/bin/env node
/**
 * v2_board_nav_informer — Regression test: Informer nav link on V2 board.
 *
 * Verifies that /v2/board/[city] contains a visible nav link that:
 *   1. Has href="/v2/informer"
 *   2. Is rendered in the correct translated text for EN, RU, ES, KA
 *   3. Appears between the Restore and How-it-works links (correct order)
 *   4. Does not overflow the header on mobile (390px) or desktop (1440px)
 *
 * Uses the V2 dev binary + Vite frontend (same boilerplate as v2_vertical_slice.js).
 * Two sequential runs required (0 FAIL, 0 SKIP).
 */

import { chromium } from 'playwright';
import { spawn, execSync } from 'child_process';
import { mkdtempSync, existsSync } from 'fs';
import { tmpdir } from 'os';
import { join, resolve } from 'path';
import net from 'net';

const ROOT = resolve(new URL('../../', import.meta.url).pathname);
const DEV_BINARY = join(tmpdir(), 'naroom-v2-dev-nav-e2e');

function buildDevBinary() {
  console.log('  Building V2 dev binary...');
  execSync(`go build -o ${DEV_BINARY} ./cmd/naroom-v2-dev/`, {
    cwd: ROOT,
    stdio: ['ignore', 'ignore', 'inherit'],
    timeout: 120000,
  });
  console.log(`  ✓ Binary built: ${DEV_BINARY}`);
}
buildDevBinary();

// ── Helpers ───────────────────────────────────────────────────────────────────

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
  return await tryHost('127.0.0.1') || await tryHost('::1');
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
  console.warn(`  ⚠ Port ${port} still open after ${timeout}ms`);
}

class ManagedProcess {
  constructor(label) { this.label = label; this.proc = null; this.port = null; this._outLines = []; }
  start(cmd, args, env = {}) {
    this.proc = spawn(cmd, args, {
      cwd: ROOT,
      env: { ...process.env, ...env },
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    this.proc.stdout.on('data', d => { this._outLines.push(...d.toString().split('\n')); });
    this.proc.stderr.on('data', d => { this._outLines.push(...d.toString().split('\n')); });
    return this;
  }
  kill() { if (this.proc) { try { this.proc.kill('SIGKILL'); } catch {} this.proc = null; } }
}

async function startTestEnv() {
  const backendPort  = await findFreePort();
  const frontendPort = await findFreePort();

  const backendProc = new ManagedProcess('v2-backend');
  const tmpDb = join(mkdtempSync(join(tmpdir(), 'v2-nav-e2e-')), 'naroom.db');

  backendProc.proc = spawn(DEV_BINARY, [], {
    cwd: ROOT,
    env: { ...process.env, DEV_PORT: String(backendPort), DEV_DB_PATH: tmpDb },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  backendProc.proc.stdout.on('data', d => { backendProc._outLines.push(...d.toString().split('\n')); });
  await waitForPort(backendPort, { label: 'v2-backend', timeout: 15000 });

  const viteEntry = join(ROOT, 'frontend', 'node_modules', 'vite', 'bin', 'vite.js');
  const frontendProc = new ManagedProcess('vite-frontend');
  frontendProc.proc = spawn(process.execPath, [viteEntry, 'dev', '--port', String(frontendPort)], {
    cwd: join(ROOT, 'frontend'),
    env: {
      ...process.env,
      BACKEND_URL_V2: `http://127.0.0.1:${backendPort}`,
      BACKEND_URL:    `http://127.0.0.1:${backendPort}`,
      NO_COLOR: '1',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  frontendProc.proc.stdout.on('data', d => { frontendProc._outLines.push(...d.toString().split('\n')); });
  frontendProc.proc.stderr.on('data', d => { frontendProc._outLines.push(...d.toString().split('\n')); });
  await waitForPort(frontendPort, { label: 'vite-frontend', timeout: 60000 });

  return {
    backendPort, frontendPort, backendProc, frontendProc, tmpDb,
    frontendBase: `http://localhost:${frontendPort}`,
  };
}

async function teardown(env) {
  env.backendProc.kill();
  if (env.frontendProc.proc) { try { env.frontendProc.proc.kill('SIGKILL'); } catch {} }
  await sleep(1500);
  await assertPortClosed(env.backendPort,  { timeout: 5000 });
  await assertPortClosed(env.frontendPort, { timeout: 5000 });
  console.log(`  ✓ processes stopped`);
}

function assert(cond, msg) { if (!cond) throw new Error(`Assertion failed: ${msg}`); }

// ── Expected translations ─────────────────────────────────────────────────────
// lang param is appended as ?lang=XX (supported by i18n.js reactive store via URL)
const NAV_CASES = [
  { lang: 'en', expected: 'Informer'      },
  { lang: 'ru', expected: 'Информер'      },
  { lang: 'es', expected: 'Informador'    },
  { lang: 'ka', expected: 'ინფორმატორი' },
];

// ── Main ──────────────────────────────────────────────────────────────────────

async function runOnce(runNumber) {
  console.log(`\n  ── Run ${runNumber} ──────────────────────────────────────────`);
  const env = await startTestEnv();
  const { frontendBase } = env;
  const browser = await chromium.launch({ headless: true });
  let passed = true;

  try {
    // ── Step 1: nav link exists with correct href ─────────────────────────────
    console.log('  Step 1: nav link href on /v2/board/tbilisi');
    {
      const page = await browser.newPage();
      await page.setViewportSize({ width: 1440, height: 900 });
      await page.goto(`${frontendBase}/v2/board/tbilisi`, { waitUntil: 'networkidle' });

      const informerLink = page.locator('nav a[href="/v2/informer"]');
      const count = await informerLink.count();
      assert(count === 1, `expected 1 nav link to /v2/informer, got ${count}`);
      console.log('    ✓ href="/v2/informer" present');

      // ── Step 2: nav order (restore → informer → how-it-works) ──────────────
      console.log('  Step 2: nav order — restore < informer < how-it-works');
      const navLinks = await page.locator('nav a').all();
      const hrefs = await Promise.all(navLinks.map(a => a.getAttribute('href')));
      const idxRestore    = hrefs.indexOf('/v2/restore');
      const idxInformer   = hrefs.indexOf('/v2/informer');
      const idxHowItWorks = hrefs.indexOf('/v2/how-it-works');
      assert(idxRestore    >= 0,              'restore link not found in nav');
      assert(idxInformer   >= 0,              'informer link not found in nav');
      assert(idxHowItWorks >= 0,              'how-it-works link not found in nav');
      assert(idxRestore < idxInformer,        'restore must come before informer');
      assert(idxInformer < idxHowItWorks,     'informer must come before how-it-works');
      console.log(`    ✓ order: restore[${idxRestore}] < informer[${idxInformer}] < how-it-works[${idxHowItWorks}]`);

      await page.close();
    }

    // ── Step 3: translations ──────────────────────────────────────────────────
    console.log('  Step 3: translations');
    for (const { lang, expected } of NAV_CASES) {
      const page = await browser.newPage();
      await page.setViewportSize({ width: 1440, height: 900 });
      // i18n is stored in localStorage — set it before navigation
      await page.goto(`${frontendBase}/v2/board/tbilisi`, { waitUntil: 'domcontentloaded' });
      await page.evaluate((l) => { localStorage.setItem('naroom_lang', l); }, lang);
      await page.goto(`${frontendBase}/v2/board/tbilisi`, { waitUntil: 'networkidle' });

      const informerLink = page.locator('nav a[href="/v2/informer"]');
      const text = (await informerLink.innerText()).trim();
      assert(text === expected, `lang=${lang}: expected "${expected}", got "${text}"`);
      console.log(`    ✓ lang=${lang}: "${text}"`);
      await page.close();
    }

    // ── Step 4: no overflow on mobile (390px) ─────────────────────────────────
    console.log('  Step 4: mobile layout — no overflow');
    {
      const page = await browser.newPage();
      await page.setViewportSize({ width: 390, height: 844 });
      // worst case: Russian
      await page.goto(`${frontendBase}/v2/board/tbilisi`, { waitUntil: 'domcontentloaded' });
      await page.evaluate(() => { localStorage.setItem('naroom_lang', 'ru'); });
      await page.goto(`${frontendBase}/v2/board/tbilisi`, { waitUntil: 'networkidle' });

      const header    = page.locator('header');
      const navEl     = page.locator('nav');
      const headerBox = await header.boundingBox();
      const navBox    = await navEl.boundingBox();

      assert(headerBox !== null, 'header not found');
      assert(navBox    !== null, 'nav not found');

      // nav right edge must not exceed header right edge (allow 2px rounding)
      const navRight    = navBox.x + navBox.width;
      const headerRight = headerBox.x + headerBox.width;
      assert(
        navRight <= headerRight + 2,
        `nav overflows header: navRight=${navRight.toFixed(1)} > headerRight=${headerRight.toFixed(1)}`
      );
      console.log(`    ✓ no overflow: navRight=${navRight.toFixed(1)} ≤ headerRight=${headerRight.toFixed(1)}`);
      await page.close();
    }

    // ── Step 5: clicking the link navigates to /v2/informer ──────────────────
    console.log('  Step 5: click navigates to /v2/informer');
    {
      const page = await browser.newPage();
      await page.setViewportSize({ width: 1440, height: 900 });
      await page.goto(`${frontendBase}/v2/board/tbilisi`, { waitUntil: 'networkidle' });
      await page.locator('nav a[href="/v2/informer"]').click();
      await page.waitForURL('**/v2/informer**', { timeout: 5000 });
      assert(
        page.url().includes('/v2/informer'),
        `expected URL to contain /v2/informer, got ${page.url()}`
      );
      console.log(`    ✓ navigated to ${page.url()}`);
      await page.close();
    }

    console.log(`\n  ✓ Run ${runNumber} PASSED (5 steps)`);
  } catch (err) {
    console.error(`\n  ✗ Run ${runNumber} FAILED: ${err.message}`);
    passed = false;
  } finally {
    await browser.close();
    await teardown(env);
  }

  return passed;
}

// Two sequential runs
const r1 = await runOnce(1);
const r2 = await runOnce(2);

console.log('\n══════════════════════════════════');
if (r1 && r2) {
  console.log('  RESULT: ALL PASSED ✓');
} else {
  console.error('  RESULT: FAILED ✗');
  process.exit(1);
}
