#!/usr/bin/env node
/**
 * V2 Board Share — focused regression tests for the "Share board" /
 * "Send to someone" distribution actions on the public /v2/board/{city}
 * page: link building (allowlisted city, exact UTM, no private data),
 * Web Share API (correct args, AbortError is not an error), clipboard
 * fallback (correct copied value + confirmation), EN/RU/ES/KA, layout
 * (no shift/overflow), and SEO cleanliness (no UTM leaking into
 * canonical/hreflang/OG/sitemap). Reuses the same dev-backend +
 * Vite-frontend harness pattern as e2e/tests/v2_browser_e2e.js /
 * e2e/tests/v2_seo_discovery.js.
 */

import { chromium } from 'playwright';
import { spawn, execSync } from 'child_process';
import { mkdtempSync, readFileSync } from 'fs';
import { tmpdir } from 'os';
import { join, resolve } from 'path';
import net from 'net';
import { buildBoardShareUrl, isKnownCity } from '../../frontend/src/lib/shareLinks.js';

const ROOT = resolve(new URL('../../', import.meta.url).pathname);

const DEV_BINARY = join(tmpdir(), 'naroom-v2-board-share-e2e');
console.log('  Building V2 dev binary...');
execSync(`go build -o ${DEV_BINARY} ./cmd/naroom-v2-dev/`, {
  cwd: ROOT,
  stdio: ['ignore', 'ignore', 'inherit'],
  timeout: 120000,
});
console.log(`  ✓ Binary: ${DEV_BINARY}`);

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

async function findFreePort() {
  return new Promise((resolvePort, reject) => {
    const srv = net.createServer();
    srv.listen(0, '127.0.0.1', () => {
      const port = srv.address().port;
      srv.close(() => resolvePort(port));
    });
    srv.on('error', reject);
  });
}

async function isPortOpen(port) {
  async function tryHost(host) {
    return new Promise(r => {
      const c = net.createConnection(port, host);
      c.setTimeout(500);
      c.on('connect', () => { c.destroy(); r(true); });
      c.on('error', () => r(false));
      c.on('timeout', () => { c.destroy(); r(false); });
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
  throw new Error(`timeout waiting for ${label} port ${port}`);
}

async function assertPortClosed(port, { timeout = 5000 } = {}) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (!(await isPortOpen(port))) return;
    await sleep(200);
  }
  throw new Error(`port ${port} did not close`);
}

function assert(cond, msg) {
  if (!cond) throw new Error(msg);
}

async function startTestEnv() {
  const backendPort = await findFreePort();
  const frontendPort = await findFreePort();
  const tmpDb = join(mkdtempSync(join(tmpdir(), 'v2-board-share-e2e-')), 'naroom-v2.db');

  const backend = spawn(DEV_BINARY, [], {
    cwd: ROOT,
    env: { ...process.env, DEV_PORT: String(backendPort), DEV_DB_PATH: tmpDb },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
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

const LANGS = { en: 'Share board', ru: 'Поделиться доской', es: 'Compartir tablero', ka: 'დაფის გაზიარება' };
const SENSITIVE_TERMS = [
  'wallet', 'token', 'recovery_code', 'management_code', 'purchase_token',
  'browser_token', 'telegram', 'signal', 'chat_id', 'contact',
];

async function main() {
  let passed = 0, failed = 0;
  const failures = [];
  function unitStep(name, fn) {
    try {
      fn();
      console.log(`  ✓ ${name}`);
      passed++;
    } catch (e) {
      console.error(`  ✗ ${name}: ${e.message}`);
      failed++;
      failures.push({ name, error: e.message });
    }
  }

  // ── 1. Link-building unit tests (pure, no server) ─────────────────────────
  const KNOWN_CITIES = [{ id: 'tbilisi', label: 'Tbilisi' }, { id: 'batumi', label: 'Batumi' }];

  unitStep('buildBoardShareUrl: correct city, exact UTM (copy_link)', () => {
    const url = new URL(buildBoardShareUrl('tbilisi', KNOWN_CITIES, 'copy_link'));
    assert(url.origin === 'https://naroom.net', `wrong origin: ${url.origin}`);
    assert(url.pathname === '/v2/board/tbilisi', `wrong path: ${url.pathname}`);
    assert(url.searchParams.get('utm_source') === 'copy_link', 'wrong utm_source');
    assert(url.searchParams.get('utm_medium') === 'referral', 'wrong utm_medium');
    assert(url.searchParams.get('utm_campaign') === 'board_share', 'wrong utm_campaign');
    assert(url.searchParams.get('utm_content') === 'tbilisi', 'wrong utm_content');
    assert([...url.searchParams.keys()].length === 4, `expected exactly 4 query params, got ${[...url.searchParams.keys()].join(',')}`);
  });

  unitStep('buildBoardShareUrl: correct city, exact UTM (native_share)', () => {
    const url = new URL(buildBoardShareUrl('tbilisi', KNOWN_CITIES, 'native_share'));
    assert(url.searchParams.get('utm_source') === 'native_share', 'wrong utm_source');
    assert(url.searchParams.get('utm_content') === 'tbilisi', 'wrong utm_content');
  });

  unitStep('buildBoardShareUrl: different city produces a different, correctly-tagged URL', () => {
    const url = new URL(buildBoardShareUrl('batumi', KNOWN_CITIES, 'copy_link'));
    assert(url.pathname === '/v2/board/batumi', `wrong path: ${url.pathname}`);
    assert(url.searchParams.get('utm_content') === 'batumi', 'wrong utm_content');
  });

  unitStep('buildBoardShareUrl: unknown city is rejected (returns null)', () => {
    assert(buildBoardShareUrl('not_a_real_city', KNOWN_CITIES, 'copy_link') === null, 'unknown city must return null, not a URL');
  });
  unitStep('buildBoardShareUrl: empty/garbage city is rejected', () => {
    assert(buildBoardShareUrl('', KNOWN_CITIES, 'copy_link') === null, 'empty city must return null');
    assert(buildBoardShareUrl('../../etc/passwd', KNOWN_CITIES, 'copy_link') === null, 'path-traversal-shaped city must return null');
  });
  unitStep('isKnownCity: matches registry exactly, rejects unlisted/garbage', () => {
    assert(isKnownCity('tbilisi', KNOWN_CITIES) === true, 'tbilisi must be known');
    assert(isKnownCity('nowhere', KNOWN_CITIES) === false, 'nowhere must not be known');
    assert(isKnownCity('tbilisi', []) === false, 'empty registry must reject everything');
    assert(isKnownCity('tbilisi', null) === false, 'null registry must reject everything, not throw');
  });

  unitStep('built URL contains no tokens, wallet, or listing ID — only city slug + fixed UTM', () => {
    const url = buildBoardShareUrl('tbilisi', KNOWN_CITIES, 'copy_link');
    for (const term of SENSITIVE_TERMS) {
      assert(!url.toLowerCase().includes(term), `share URL must not contain "${term}": ${url}`);
    }
    const parsed = new URL(url);
    assert(!/\/v2\/listing\//.test(parsed.pathname), 'share URL must never reference a listing path');
  });

  const env = await startTestEnv();
  const { frontendBase } = env;

  async function step(name, fn) {
    try {
      await fn();
      console.log(`  ✓ ${name}`);
      passed++;
    } catch (e) {
      console.error(`  ✗ ${name}: ${e.message}`);
      failed++;
      failures.push({ name, error: e.message });
    }
  }

  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({ permissions: ['clipboard-read', 'clipboard-write'] });

  // ── 5. Layout: no shift/overflow, buttons visible, cards untouched ─────────
  // Runs early (right after browser/context setup), before the many
  // navigations the share/clipboard/lang tests below make against the SAME
  // city — /v2/board/{city} shares one 30-requests/minute rate-limit bucket
  // per visitor (internal/v2/listing_http.go: boardLim) across ALL of this
  // test file's navigations, and each navigation triggers the board page's
  // own pre-existing client-side loadBoard() re-fetch on top of the SSR
  // fetch. Running the layout checks first keeps them well clear of that
  // budget instead of racing it.
  // context.newPage() does not accept a viewport option (that's only valid on
  // browser.newContext()) — a dedicated context per viewport is required to
  // actually get the requested size.
  for (const viewport of [{ w: 1440, h: 900 }, { w: 390, h: 844 }]) {
    await step(`layout ${viewport.w}x${viewport.h}: no horizontal overflow, share row visible, grid unshifted`, async () => {
      const vpContext = await browser.newContext({ viewport: { width: viewport.w, height: viewport.h } });
      const page = await vpContext.newPage();
      try {
        await page.goto(`${frontendBase}/v2/board/tbilisi`, { waitUntil: 'networkidle' });
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1);
        assert(!overflow, `horizontal overflow at ${viewport.w}x${viewport.h}`);
        const shareRow = page.locator('.share-row');
        await shareRow.waitFor({ state: 'visible', timeout: 5000 });
        const shareBox = await shareRow.boundingBox();
        const gridBox = await page.locator('.grid').boundingBox();
        assert(shareBox && gridBox, 'share row or grid not found');
        assert(shareBox.y + shareBox.height <= gridBox.y + 1, `share row (bottom ${shareBox.y + shareBox.height}) must sit above the grid (top ${gridBox.y}), not overlap it`);
        const tabsBox = await page.locator('.tabs').boundingBox();
        assert(tabsBox.y + tabsBox.height <= shareBox.y + 1, 'share row must sit below the city tabs, not overlap them');
      } finally {
        await page.close();
        await vpContext.close();
      }
    });
  }

  // ── 2. Web Share API stub ──────────────────────────────────────────────────
  await step('Send to someone: calls navigator.share with correct title/text/url', async () => {
    const page = await context.newPage();
    try {
      await page.addInitScript(() => {
        window.__shareCalls = [];
        navigator.share = (data) => { window.__shareCalls.push(data); return Promise.resolve(); };
      });
      await page.goto(`${frontendBase}/v2/board/tbilisi`, { waitUntil: 'networkidle' });
      await page.click('button.share-btn:has-text("Send to someone")');
      await page.waitForTimeout(300);
      const calls = await page.evaluate(() => window.__shareCalls);
      assert(calls.length === 1, `expected navigator.share called exactly once, got ${calls.length}`);
      const call = calls[0];
      assert(typeof call.title === 'string' && call.title.length > 0, 'share call missing a title');
      assert(typeof call.text === 'string' && /cannabis/i.test(call.text) && /tbilisi/i.test(call.text), `share text should mention cannabis + city, got: ${call.text}`);
      assert(call.url.startsWith('https://naroom.net/v2/board/tbilisi'), `share url wrong: ${call.url}`);
      assert(call.url.includes('utm_source=native_share'), `share url missing native_share utm_source: ${call.url}`);
      for (const term of SENSITIVE_TERMS) {
        assert(!call.url.toLowerCase().includes(term) && !call.text.toLowerCase().includes(term), `share call must not contain "${term}"`);
      }
    } finally {
      await page.close();
    }
  });

  await step('Send to someone: navigator.share AbortError (user cancelled) shows no error, label unchanged', async () => {
    const page = await context.newPage();
    try {
      await page.addInitScript(() => {
        navigator.share = () => Promise.reject(new DOMException('cancelled', 'AbortError'));
      });
      let pageError = null;
      page.on('pageerror', (e) => { pageError = e; });
      await page.goto(`${frontendBase}/v2/board/tbilisi`, { waitUntil: 'networkidle' });
      const btn = page.locator('button.share-btn:has-text("Send to someone")');
      await btn.click();
      await page.waitForTimeout(500);
      assert(!pageError, `AbortError must not surface as a page error, got: ${pageError}`);
      const label = await btn.textContent();
      assert(label.trim() === 'Send to someone', `cancelling must not change the button label, got: "${label.trim()}"`);
    } finally {
      await page.close();
    }
  });

  // ── 3. Clipboard fallback ──────────────────────────────────────────────────
  await step('Share board: copies the exact link to clipboard and shows "Link copied"', async () => {
    const page = await context.newPage();
    try {
      await page.goto(`${frontendBase}/v2/board/tbilisi`, { waitUntil: 'networkidle' });
      await page.click('button.share-btn:has-text("Share board")');
      await page.waitForTimeout(300);
      const label = await page.locator('button.share-btn').first().textContent();
      assert(label.trim() === 'Link copied', `expected "Link copied", got "${label.trim()}"`);
      const clip = await page.evaluate(() => navigator.clipboard.readText());
      assert(clip === 'https://naroom.net/v2/board/tbilisi?utm_source=copy_link&utm_medium=referral&utm_campaign=board_share&utm_content=tbilisi', `unexpected clipboard content: ${clip}`);
    } finally {
      await page.close();
    }
  });

  await step('Send to someone: when navigator.share is unavailable, copies text+link and shows "Message copied"', async () => {
    const page = await context.newPage();
    try {
      await page.addInitScript(() => {
        // navigator.share is a non-configurable Navigator.prototype accessor
        // in Chromium — `delete navigator.share` silently no-ops. Shadow it
        // with an own, undefined property instead, matching how the
        // navigator.clipboard-unavailable tests below do the same thing.
        Object.defineProperty(navigator, 'share', { value: undefined, configurable: true });
      });
      await page.goto(`${frontendBase}/v2/board/tbilisi`, { waitUntil: 'networkidle' });
      // Index-based, not :has-text(...) — the button's own text changes on
      // click (that's what this test verifies), and a :has-text() locator
      // re-matches on every subsequent action, so it stops matching this
      // exact button the moment its label changes.
      const btn = page.locator('button.share-btn').nth(1);
      await btn.click();
      await page.waitForTimeout(300);
      const label = await btn.textContent();
      assert(label.trim() === 'Message copied', `expected "Message copied", got "${label.trim()}"`);
      const clip = await page.evaluate(() => navigator.clipboard.readText());
      assert(clip.includes('https://naroom.net/v2/board/tbilisi?utm_source=native_share'), `clipboard must contain the native_share link, got: ${clip}`);
      assert(/cannabis/i.test(clip) && /tbilisi/i.test(clip), `clipboard text must mention cannabis + city, got: ${clip}`);
    } finally {
      await page.close();
    }
  });

  await step('Share board: falls back to execCommand when navigator.clipboard is unavailable, still confirms success', async () => {
    const page = await context.newPage();
    try {
      await page.addInitScript(() => {
        Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true });
        window.__execCopyCalls = 0;
        const realExec = document.execCommand.bind(document);
        document.execCommand = (cmd, ...rest) => {
          if (cmd === 'copy') window.__execCopyCalls++;
          return realExec(cmd, ...rest);
        };
      });
      await page.goto(`${frontendBase}/v2/board/tbilisi`, { waitUntil: 'networkidle' });
      await page.click('button.share-btn:has-text("Share board")');
      await page.waitForTimeout(300);
      const calls = await page.evaluate(() => window.__execCopyCalls);
      assert(calls >= 1, 'expected the execCommand(\'copy\') fallback path to run when navigator.clipboard is unavailable');
      const label = await page.locator('button.share-btn').first().textContent();
      assert(label.trim() === 'Link copied', `expected "Link copied" via the fallback path, got "${label.trim()}"`);
    } finally {
      await page.close();
    }
  });

  await step('Share board: shows the localized copy-error message when both clipboard paths fail', async () => {
    const page = await context.newPage();
    try {
      await page.addInitScript(() => {
        Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true });
        document.execCommand = () => false; // force both paths to fail
      });
      await page.goto(`${frontendBase}/v2/board/tbilisi`, { waitUntil: 'networkidle' });
      await page.click('button.share-btn:has-text("Share board")');
      await page.waitForTimeout(300);
      const label = await page.locator('button.share-btn').first().textContent();
      assert(!/^Link copied$/.test(label.trim()), `must not falsely claim success, got "${label.trim()}"`);
      // Must be the honest, exact copy-error text — not a stale message that
      // tells the user to "copy the link manually" while never showing them
      // the link anywhere on the page.
      assert(label.trim() === "Couldn't copy the link", `expected the exact honest copy-error message, got "${label.trim()}"`);
      assert(!/manually/i.test(label), `copy-error message must not instruct a manual copy that has nowhere to happen, got "${label.trim()}"`);
    } finally {
      await page.close();
    }
  });

  // ── 4. EN/RU/ES/KA ─────────────────────────────────────────────────────────
  for (const [lang, expectedShareLabel] of Object.entries(LANGS)) {
    await step(`?lang=${lang}: share buttons render with the correct localized label`, async () => {
      const page = await context.newPage();
      try {
        await page.goto(`${frontendBase}/v2/board/tbilisi?lang=${lang}`, { waitUntil: 'networkidle' });
        const label = await page.locator('button.share-btn').first().textContent();
        assert(label.trim() === expectedShareLabel, `lang=${lang}: expected "${expectedShareLabel}", got "${label.trim()}"`);
      } finally {
        await page.close();
      }
    });
  }

  await browser.close();

  // ── 6. SEO cleanliness: no UTM in canonical/hreflang/OG/sitemap ────────────
  await step('canonical/hreflang/OG on /v2/board/tbilisi carry no UTM parameters', async () => {
    const res = await fetch(`${frontendBase}/v2/board/tbilisi`);
    const html = await res.text();
    const canonical = html.match(/<link rel="canonical" href="([^"]+)"/);
    assert(canonical && !canonical[1].includes('utm_'), `canonical must not carry UTM: ${canonical && canonical[1]}`);
    const ogUrl = html.match(/<meta property="og:url" content="([^"]+)"/);
    assert(ogUrl && !ogUrl[1].includes('utm_'), `og:url must not carry UTM: ${ogUrl && ogUrl[1]}`);
    const hreflangs = [...html.matchAll(/<link rel="alternate" hreflang="[^"]+" href="([^"]+)"/g)];
    assert(hreflangs.length > 0 && hreflangs.every(h => !h[1].includes('utm_')), 'no hreflang href may carry UTM');
    const jsonLdBlocks = [...html.matchAll(/<script type="application\/ld\+json">([\s\S]*?)<\/script>/g)];
    for (const b of jsonLdBlocks) {
      const obj = JSON.parse(b[1]);
      assert(!String(obj.url || '').includes('utm_'), `JSON-LD url must not carry UTM: ${obj.url}`);
    }
  });

  await step('sitemap.xml carries no UTM parameters anywhere', () => {
    const xml = readFileSync(join(ROOT, 'frontend/static/sitemap.xml'), 'utf8');
    assert(!xml.includes('utm_'), 'sitemap.xml must not contain any UTM parameter');
  });

  await teardown(env);

  console.log(`\n  TOTAL: ${passed} passed, ${failed} failed`);
  if (failed > 0) {
    console.log('  Failures:');
    for (const f of failures) console.log(`    - ${f.name}: ${f.error}`);
    process.exit(1);
  }
  process.exit(0);
}

main().catch(e => {
  console.error('FATAL:', e);
  process.exit(1);
});
