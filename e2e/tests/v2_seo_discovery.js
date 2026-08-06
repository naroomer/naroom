#!/usr/bin/env node
/**
 * V2 SEO / Discovery — focused regression tests for the indexation/discovery
 * SEO task, including the acceptance-repair pass: robots.txt (Allow HTML
 * pages, Disallow only /api/), sitemap.xml (all EnabledCities(), 14 URLs),
 * llms.txt, exact-count-1 metadata dedup on the two SEO-managed routes,
 * fixed https://naroom.net canonical/hreflang/OG regardless of request host,
 * SSR client-IP forwarding safety, URL-authoritative language (persists
 * after hydration, survives navigation, switcher updates the URL), SSR
 * <html lang>, and no double-$/raw-placeholder/stale wording across
 * EN/RU/ES/KA. Uses the same dev-backend + Vite-frontend harness pattern as
 * e2e/tests/v2_browser_e2e.js (see that file for the full rationale of the
 * devAPI/step/startTestEnv conventions reused here).
 */

import { chromium } from 'playwright';
import { spawn, execSync } from 'child_process';
import { mkdtempSync, mkdirSync, existsSync, readFileSync } from 'fs';
import { tmpdir } from 'os';
import { join, resolve } from 'path';
import net from 'net';
import http from 'http';
import { buildForwardedForHeaders } from '../../frontend/src/lib/server/ssrClientIp.js';

const ROOT = resolve(new URL('../../', import.meta.url).pathname);

const DEV_BINARY = join(tmpdir(), 'naroom-v2-seo-e2e');
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
  // Vite's dev server binds ::1 (IPv6) only by default on this machine — check
  // both hosts, matching e2e/tests/v2_browser_e2e.js's isPortOpen.
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
  const tmpDb = join(mkdtempSync(join(tmpdir(), 'v2-seo-e2e-')), 'naroom-v2.db');

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

// A minimal stand-in for the Go backend that only records the headers of the
// last request it received, used to prove — end to end, through the REAL
// frontend SSR load function — that board/[city]/+page.server.js forwards
// the correct X-Forwarded-For value to its backend fetch, without needing
// the real rate limiter to observe that specific wiring.
function startStubBackend() {
  let lastHeaders = null;
  const server = http.createServer((req, res) => {
    lastHeaders = req.headers;
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(req.url.startsWith('/v2/board/cities') ? '[]' : '[]');
  });
  return new Promise((resolvePromise) => {
    server.listen(0, '127.0.0.1', () => {
      const port = server.address().port;
      resolvePromise({
        server,
        base: `http://127.0.0.1:${port}`,
        lastHeaders: () => lastHeaders,
        close: () => new Promise((r) => server.close(r)),
      });
    });
  });
}

// Spawns a standalone Vite frontend pointed at an arbitrary backend URL (the
// stub above, or anything else) — reuses the exact same spawn/env pattern as
// startTestEnv(), minus the Go backend, since this sub-test only needs to
// observe what the frontend's SSR fetch sends, not real board data.
async function startFrontendOnly(backendUrl) {
  const frontendPort = await findFreePort();
  const viteEntry = join(ROOT, 'frontend', 'node_modules', 'vite', 'bin', 'vite.js');
  const frontend = spawn(
    process.execPath,
    [viteEntry, 'dev', '--port', String(frontendPort)],
    {
      cwd: join(ROOT, 'frontend'),
      env: { ...process.env, BACKEND_URL_V2: backendUrl, BACKEND_URL: backendUrl, NO_COLOR: '1' },
      stdio: ['ignore', 'pipe', 'pipe'],
    }
  );
  await waitForPort(frontendPort, { label: 'stub-fronted frontend', timeout: 60000 });
  return {
    frontend,
    frontendPort,
    frontendBase: `http://localhost:${frontendPort}`,
    close: async () => {
      try { frontend.kill('SIGKILL'); } catch {}
      await sleep(1000);
      await assertPortClosed(frontendPort, { timeout: 5000 });
    },
  };
}

const LANGS = ['en', 'ru', 'es', 'ka'];
const PRIVATE_PATHS = ['/v2/new', '/v2/restore', '/v2/informer', '/v2/listing/sample_cannabis', '/v2/helper/purchase', '/v2/helper/purchases'];
const STALE_TERMS = [
  { re: /\bbitcoin\b/i, name: 'Bitcoin' },
  { re: /\bBTC\b/, name: 'BTC' },
  { re: /encrypted chat/i, name: 'encrypted chat' },
  { re: /\btor\b/i, name: 'Tor' },
  { re: /onion/i, name: 'onion' },
  { re: /\$15\b/, name: '$15' },
  { re: /no personal data is collected/i, name: '"no personal data is collected"' },
];
const SITE_ORIGIN = 'https://naroom.net';

function countOccurrences(html, re) {
  return (html.match(re) || []).length;
}

async function main() {
  // ── Focused unit test: SSR client-IP forwarding (point 5) — no server needed ──
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

  // buildForwardedForHeaders(immediateAddress, xForwardedFor, xRealIp) —
  // mirrors internal/v2/clientip.go's trust boundary exactly (see
  // frontend/src/lib/server/ssrClientIp.js for the full rationale).

  unitStep('empty immediate address + spoofed valid X-Forwarded-For: NOT confirmed loopback, header ignored, returns {}', () => {
    const h = buildForwardedForHeaders('', '203.0.113.77', null);
    assert(Object.keys(h).length === 0, `an empty/missing immediate address must never fall into the trusted-proxy-hop path, got ${JSON.stringify(h)}`);
  });
  unitStep('malformed immediate address + spoofed valid X-Forwarded-For: NOT confirmed loopback, header ignored, returns {}', () => {
    const h = buildForwardedForHeaders('not-an-ip-at-all', '203.0.113.78', null);
    assert(Object.keys(h).length === 0, `a malformed immediate address must never fall into the trusted-proxy-hop path, got ${JSON.stringify(h)}`);
  });
  unitStep('immediate peer loopback (127.0.0.1) + no proxy headers: omits header entirely (honest fallback, not a false fix)', () => {
    assert(Object.keys(buildForwardedForHeaders('127.0.0.1', null, null)).length === 0, 'must omit header when no real visitor IP is available');
  });
  unitStep('immediate peer loopback (::1) + no proxy headers: omits header entirely', () => {
    assert(Object.keys(buildForwardedForHeaders('::1', null, null)).length === 0, 'must omit header for ::1 with no proxy headers');
  });
  unitStep('immediate peer loopback + valid X-Forwarded-For: uses the LAST valid entry in the chain', () => {
    const h = buildForwardedForHeaders('127.0.0.1', '198.51.100.20, 203.0.113.30', null);
    assert(h['X-Forwarded-For'] === '203.0.113.30', `expected the last chain entry 203.0.113.30, got ${h['X-Forwarded-For']}`);
  });
  unitStep('immediate peer loopback + malformed X-Forwarded-For + valid X-Real-IP: falls back to X-Real-IP', () => {
    const h = buildForwardedForHeaders('127.0.0.1', 'not-an-ip, also-bad', '203.0.113.40');
    assert(h['X-Forwarded-For'] === '203.0.113.40', `expected X-Real-IP fallback 203.0.113.40, got ${h['X-Forwarded-For']}`);
  });
  unitStep('immediate peer loopback + X-Forwarded-For chain resolving to loopback: rejected, no header sent', () => {
    const h = buildForwardedForHeaders('127.0.0.1', '203.0.113.50, 127.0.0.1', null);
    assert(Object.keys(h).length === 0, 'a loopback value inside X-Forwarded-For must never be forwarded, even as the last chain entry');
  });
  unitStep('immediate peer loopback + empty X-Forwarded-For + loopback X-Real-IP: rejected, no header sent', () => {
    const h = buildForwardedForHeaders('127.0.0.1', '', '::1');
    assert(Object.keys(h).length === 0, 'a loopback X-Real-IP must never be forwarded');
  });
  unitStep('immediate peer NOT loopback: uses the immediate peer directly, ignores proxy headers entirely (spoofed XFF has no effect)', () => {
    const h = buildForwardedForHeaders('203.0.113.60', '198.51.100.99', '198.51.100.98');
    assert(h['X-Forwarded-For'] === '203.0.113.60', `expected the immediate peer 203.0.113.60 to win over spoofed headers, got ${h['X-Forwarded-For']}`);
  });
  unitStep('immediate peer NOT loopback + no proxy headers at all: still uses the immediate peer', () => {
    const h = buildForwardedForHeaders('203.0.113.61', null, null);
    assert(h['X-Forwarded-For'] === '203.0.113.61', `expected 203.0.113.61, got ${h['X-Forwarded-For']}`);
  });
  unitStep('two distinct visitors (loopback peer, distinct X-Forwarded-For) forward distinct, non-collapsed values', () => {
    const a = buildForwardedForHeaders('127.0.0.1', '203.0.113.5', null);
    const b = buildForwardedForHeaders('127.0.0.1', '198.51.100.7', null);
    assert(a['X-Forwarded-For'] === '203.0.113.5', `expected 203.0.113.5, got ${a['X-Forwarded-For']}`);
    assert(b['X-Forwarded-For'] === '198.51.100.7', `expected 198.51.100.7, got ${b['X-Forwarded-For']}`);
    assert(a['X-Forwarded-For'] !== b['X-Forwarded-For'], 'two distinct visitor IPs must not collapse into the same forwarded value');
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

  // ── SSR client-IP integration tests (acceptance-repair, point 3) ─────────────
  // All four requirements, proven against the REAL frontend SSR pipeline and
  // the REAL backend rate limiter — not just the buildForwardedForHeaders
  // unit tests above.

  await step('frontend SSR → backend: the real X-Forwarded-For chain reaches the backend fetch (via a recording stub backend)', async () => {
    const stub = await startStubBackend();
    const fe = await startFrontendOnly(stub.base);
    try {
      // Loopback peer (this test process, connecting to the stub-fronted
      // frontend on localhost) + a Caddy-style X-Forwarded-For chain: the
      // frontend must forward the LAST chain entry, exactly like
      // internal/v2/clientip.go would trust it on the backend's own loopback hop.
      await fetch(`${fe.frontendBase}/v2/board/tbilisi`, {
        headers: { 'X-Forwarded-For': '198.51.100.20, 203.0.113.31' },
      });
      const seen = stub.lastHeaders();
      assert(seen, 'stub backend never received a request — SSR fetch did not reach it');
      assert(seen['x-forwarded-for'] === '203.0.113.31', `stub backend saw X-Forwarded-For="${seen['x-forwarded-for']}", want "203.0.113.31" (the last chain entry)`);
    } finally {
      await fe.close();
      await stub.close();
    }
  });

  await step('frontend SSR → backend: no visitor IP available → no X-Forwarded-For sent at all (no false "fixed" header)', async () => {
    const stub = await startStubBackend();
    const fe = await startFrontendOnly(stub.base);
    try {
      await fetch(`${fe.frontendBase}/v2/board/tbilisi`); // no XFF, no X-Real-IP
      const seen = stub.lastHeaders();
      assert(seen, 'stub backend never received a request');
      assert(!seen['x-forwarded-for'], `expected no X-Forwarded-For header, got "${seen['x-forwarded-for']}"`);
    } finally {
      await fe.close();
      await stub.close();
    }
  });

  await step('backend: two distinct X-Forwarded-For values (loopback peer) map to two distinct rate-limit buckets — one IP hits its own 30/min limit at request 31', async () => {
    // Direct-to-backend, over the SAME loopback+X-Forwarded-For trust
    // boundary the SSR fetch relies on (this test process's connection to
    // 127.0.0.1:{backendPort} IS a loopback peer, exactly like the
    // frontend's own SSR fetch is).
    const ip = '203.0.113.90';
    let ok = 0, limited = 0;
    for (let i = 0; i < 31; i++) {
      const res = await fetch(`${env.backendBase}/v2/board/tbilisi`, { headers: { 'X-Forwarded-For': ip } });
      if (res.status === 200) ok++;
      else if (res.status === 429) limited++;
    }
    assert(ok === 30, `expected exactly 30 successes for a single visitor's own 30/min budget, got ${ok}`);
    assert(limited === 1, `expected the 31st request from the SAME visitor to be rate-limited (proves the limiter itself is real), got ${limited} 429s`);
  });

  await step('backend: 63 requests across 3 distinct visitor IPs (21 each, all under their own 30/min limit) lose zero board/city data — proves buckets are NOT shared', async () => {
    const ips = ['198.51.100.101', '198.51.100.102', '198.51.100.103'];
    const total = 63;
    let successes = 0;
    const statusCounts = {};
    for (let i = 0; i < total; i++) {
      const ip = ips[i % ips.length];
      const res = await fetch(`${env.backendBase}/v2/board/tbilisi`, { headers: { 'X-Forwarded-For': ip } });
      statusCounts[res.status] = (statusCounts[res.status] || 0) + 1;
      if (res.status === 200) successes++;
    }
    assert(successes === total, `expected all ${total} requests across 3 distinct visitor IPs to succeed (21 each, under the 30/min per-IP limit) — got ${successes}/${total}, status breakdown: ${JSON.stringify(statusCounts)}. A shared single bucket would start returning 429 (and the frontend would render empty board data) once the COMBINED count crossed 30.`);
  });

  await step('unit-boundary sanity: a spoofed X-Forwarded-For from a non-loopback peer never reaches the backend as trusted', () => {
    // Already covered at the function level above (immediate peer NOT
    // loopback → proxy headers ignored entirely); restated here as an
    // explicit named regression for point 3 requirement 4, so a future
    // change to ssrClientIp.js that weakens this cannot pass silently.
    const h = buildForwardedForHeaders('203.0.113.200', '10.0.0.1, 10.0.0.2', '10.0.0.3');
    assert(h['X-Forwarded-For'] === '203.0.113.200', `spoofed proxy headers must be ignored when the immediate peer is not loopback, got ${h['X-Forwarded-For']}`);
  });

  // ── sitemap.xml: all EnabledCities() (14 URLs), no Thai disabled cities, no redirects/private ──
  await step('sitemap.xml: 14 URLs from EnabledCities() (includes Nha Trang/Da Nang, excludes disabled Thai cities)', () => {
    const xml = readFileSync(join(ROOT, 'frontend/static/sitemap.xml'), 'utf8');
    assert(xml.includes('<?xml'), 'sitemap.xml missing XML declaration');
    assert(xml.includes('xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"'), 'sitemap.xml missing standard namespace');
    const locs = [...xml.matchAll(/<loc>([^<]+)<\/loc>/g)].map(m => m[1]);
    assert(locs.length === 14, `sitemap.xml has ${locs.length} <url> entries, want 14 (how-it-works + 13 enabled cities)`);
    for (const loc of locs) {
      assert(loc.startsWith('https://naroom.net/v2/'), `sitemap URL not a canonical https /v2/ URL: ${loc}`);
      assert(!loc.includes('?'), `sitemap URL must not carry a query string: ${loc}`);
    }
    for (const mustInclude of ['nha_trang', 'da_nang']) {
      assert(locs.some(l => l.includes(mustInclude)), `sitemap must include enabled city ${mustInclude}`);
    }
    for (const disabledThai of ['bangkok', 'chiang_mai', 'phuket']) {
      assert(!locs.some(l => l.includes(disabledThai)), `sitemap must not include disabled city ${disabledThai}`);
    }
    for (const bad of ['/v2/new', '/v2/restore', '/v2/informer', '/v2/listing/', '/v2/helper/']) {
      assert(!locs.some(l => l.includes(bad)), `sitemap must not contain private route ${bad}`);
    }
    assert(!xml.includes('<changefreq>') && !xml.includes('<priority>'), 'sitemap must not contain decorative changefreq/priority');
  });

  // ── robots.txt: HTML pages fetchable (only /api/ disallowed) ──
  await step('robots.txt: allows /v2/ HTML pages (crawler must see their own noindex), disallows only /api/', () => {
    const txt = readFileSync(join(ROOT, 'frontend/static/robots.txt'), 'utf8');
    assert(/Allow:\s*\/v2\//.test(txt), 'robots.txt must allow /v2/');
    for (const p of ['/v2/new', '/v2/restore', '/v2/informer', '/v2/listing/', '/v2/helper/']) {
      assert(!txt.includes(`Disallow: ${p}`), `robots.txt must NOT disallow ${p} — the crawler must fetch it to see its own noindex`);
    }
    assert(txt.includes('Disallow: /api/'), 'robots.txt must disallow /api/');
    assert(/User-agent:\s*OAI-SearchBot/i.test(txt), 'robots.txt must mention OAI-SearchBot');
    const oaiBlock = txt.slice(txt.search(/User-agent:\s*OAI-SearchBot/i));
    assert(/Allow:\s*\/v2\//.test(oaiBlock), 'OAI-SearchBot block must also allow /v2/');
    assert(oaiBlock.includes('Disallow: /api/'), 'OAI-SearchBot block must also disallow /api/');
    assert(/Sitemap:\s*https:\/\/naroom\.net\/sitemap\.xml/.test(txt), 'robots.txt must reference the sitemap');
  });

  // ── llms.txt ──
  await step('llms.txt: matches Cannabis + Litecoin product, precise privacy/role claims, no stale terms', () => {
    const txt = readFileSync(join(ROOT, 'frontend/static/llms.txt'), 'utf8');
    assert(/cannabis/i.test(txt), 'llms.txt must describe the cannabis peer-support product');
    assert(/litecoin/i.test(txt), 'llms.txt must state Litecoin');
    assert(/telegram or signal/i.test(txt) || /telegram.{0,20}signal/i.test(txt), 'llms.txt must mention Telegram or Signal contact');
    for (const term of STALE_TERMS) {
      assert(!term.re.test(txt), `llms.txt must not contain stale term: ${term.name}`);
    }
    assert(!/wallet address is (the |an )?identity/i.test(txt) && !/^identity is a litecoin/im.test(txt), 'llms.txt must not call the wallet "identity"');
    assert(/eligibility/i.test(txt), 'llms.txt must describe the wallet as used for eligibility, not identity');
    assert(/stored encrypted/i.test(txt) || /encrypted and available/i.test(txt), 'llms.txt must state the contact handle is encrypted');
    assert(/helper.{0,40}(nickname|rating)/i.test(txt), 'llms.txt must describe the client receiving the helper nickname/rating');
    assert(txt.includes('/v2/board/'), 'llms.txt must reference /v2/board/, not the old /board/ path');
    assert(txt.includes('/v2/how-it-works'), 'llms.txt must reference /v2/how-it-works, not the old /how-it-works path');
    assert(!/^- \/how-it-works /m.test(txt), 'llms.txt must not list the old bare /how-it-works path');
    assert(!/^- \/board\//m.test(txt), 'llms.txt must not list the old bare /board/ path');
  });

  // ── Metadata dedup: exact count = 1 for title/description/canonical/OG/Twitter, on both SEO-managed routes ──
  const browser = await chromium.launch({ headless: true });
  for (const path of ['/v2/how-it-works', '/v2/board/tbilisi']) {
    await step(`${path}: exactly one of each — title, description, canonical, OG, Twitter`, async () => {
      const res = await fetch(`${frontendBase}${path}`);
      const html = await res.text();
      assert(countOccurrences(html, /<title>/g) === 1, `${path}: expected exactly 1 <title>, got ${countOccurrences(html, /<title>/g)}`);
      assert(countOccurrences(html, /<meta name="description"/g) === 1, `${path}: expected exactly 1 meta description`);
      assert(countOccurrences(html, /<link rel="canonical"/g) === 1, `${path}: expected exactly 1 canonical link`);
      assert(countOccurrences(html, /<meta name="robots"/g) === 1, `${path}: expected exactly 1 meta robots`);
      assert(countOccurrences(html, /<meta property="og:title"/g) === 1, `${path}: expected exactly 1 og:title`);
      assert(countOccurrences(html, /<meta property="og:description"/g) === 1, `${path}: expected exactly 1 og:description`);
      assert(countOccurrences(html, /<meta property="og:url"/g) === 1, `${path}: expected exactly 1 og:url`);
      assert(countOccurrences(html, /<meta property="og:image"/g) === 1, `${path}: expected exactly 1 og:image`);
      assert(countOccurrences(html, /<meta property="og:locale"/g) === 1, `${path}: expected exactly 1 og:locale`);
      assert(countOccurrences(html, /<meta name="twitter:card"/g) === 1, `${path}: expected exactly 1 twitter:card`);
      assert(countOccurrences(html, /<meta name="twitter:title"/g) === 1, `${path}: expected exactly 1 twitter:title`);
      assert(countOccurrences(html, /<meta name="twitter:description"/g) === 1, `${path}: expected exactly 1 twitter:description`);
      assert(countOccurrences(html, /<meta name="twitter:image"/g) === 1, `${path}: expected exactly 1 twitter:image`);
      assert(/<meta name="robots" content="index, follow"/.test(html), `${path} missing index,follow meta robots`);
      const ogLocale = html.match(/<meta property="og:locale" content="([^"]+)"/);
      assert(ogLocale && /^[a-z]{2}_[A-Z]{2}$/.test(ogLocale[1]), `${path}: og:locale "${ogLocale && ogLocale[1]}" is not a valid xx_XX locale`);
    });
  }

  // ── Private pages: robots.txt does NOT block them, but they still self-declare noindex ──
  for (const path of PRIVATE_PATHS) {
    await step(`${path}: fetchable (not robots.txt-blocked), self-declares noindex/nofollow/noarchive`, async () => {
      const res = await fetch(`${frontendBase}${path}`);
      assert(res.status === 200, `${path} must be fetchable (200), got ${res.status} — a crawler must be able to see its own noindex tag`);
      const html = await res.text();
      assert(/<meta name="robots" content="noindex, nofollow, noarchive"/.test(html), `${path} missing noindex meta robots`);
      assert(countOccurrences(html, /<meta name="robots"/g) === 1, `${path} must have exactly one meta robots tag`);
      const xrt = res.headers.get('x-robots-tag');
      assert(xrt && xrt.includes('noindex'), `${path} missing X-Robots-Tag: noindex header`);
    });
  }

  // ── Fixed canonical origin (point 4): must be https://naroom.net even though the test server itself is on localhost ──
  await step('canonical/hreflang/OG/JSON-LD always use https://naroom.net, never the request host (localhost here)', async () => {
    for (const path of ['/v2/how-it-works', '/v2/board/tbilisi']) {
      const res = await fetch(`${frontendBase}${path}`);
      const html = await res.text();
      assert(!html.includes(frontendBase), `${path}: HTML must not leak the test request host ${frontendBase}`);
      const canonical = html.match(/<link rel="canonical" href="([^"]+)"/);
      assert(canonical && canonical[1].startsWith(SITE_ORIGIN), `${path}: canonical "${canonical && canonical[1]}" must start with ${SITE_ORIGIN}`);
      const ogUrl = html.match(/<meta property="og:url" content="([^"]+)"/);
      assert(ogUrl && ogUrl[1].startsWith(SITE_ORIGIN), `${path}: og:url must start with ${SITE_ORIGIN}`);
      const ogImage = html.match(/<meta property="og:image" content="([^"]+)"/);
      assert(ogImage && ogImage[1].startsWith(SITE_ORIGIN), `${path}: og:image must start with ${SITE_ORIGIN}`);
      const hreflangs = [...html.matchAll(/<link rel="alternate" hreflang="[^"]+" href="([^"]+)"/g)];
      assert(hreflangs.length > 0 && hreflangs.every(h => h[1].startsWith(SITE_ORIGIN)), `${path}: every hreflang href must start with ${SITE_ORIGIN}`);
      const jsonLdBlocks = [...html.matchAll(/<script type="application\/ld\+json">([\s\S]*?)<\/script>/g)];
      for (const b of jsonLdBlocks) {
        const obj = JSON.parse(b[1]);
        assert(String(obj.url || '').startsWith(SITE_ORIGIN), `${path}: JSON-LD url must start with ${SITE_ORIGIN}, got ${obj.url}`);
      }
    }
  });

  // ── canonical and sitemap do not conflict ──
  await step('canonical of /v2/how-it-works and /v2/board/tbilisi match their sitemap.xml entries', async () => {
    const xml = readFileSync(join(ROOT, 'frontend/static/sitemap.xml'), 'utf8');
    for (const [path, expectedSuffix] of [['/v2/how-it-works', '/v2/how-it-works'], ['/v2/board/tbilisi', '/v2/board/tbilisi']]) {
      const res = await fetch(`${frontendBase}${path}`);
      const html = await res.text();
      const m = html.match(/<link rel="canonical" href="([^"]+)"/);
      assert(m, `${path} missing canonical`);
      assert(m[1] === `${SITE_ORIGIN}${expectedSuffix}`, `canonical ${m[1]} does not equal ${SITE_ORIGIN}${expectedSuffix}`);
      assert(xml.includes(`>${SITE_ORIGIN}${expectedSuffix}<`), `sitemap.xml missing matching entry for ${expectedSuffix}`);
    }
  });

  // ── hreflang: complete and reciprocal ──
  for (const path of ['/v2/how-it-works', '/v2/board/tbilisi']) {
    await step(`${path}: hreflang complete (en/ru/es/ka/x-default) and reciprocal`, async () => {
      const res = await fetch(`${frontendBase}${path}`);
      const html = await res.text();
      const tags = [...html.matchAll(/<link rel="alternate" hreflang="([^"]+)" href="([^"]+)"/g)];
      const langs = tags.map(t => t[1]).sort();
      assert(JSON.stringify(langs) === JSON.stringify(['en', 'es', 'ka', 'ru', 'x-default']), `hreflang set incomplete: ${langs.join(',')}`);
      for (const l of ['ru', 'es', 'ka']) {
        const tag = tags.find(t => t[1] === l);
        assert(tag && tag[2].includes(`?lang=${l}`), `hreflang ${l} href does not carry ?lang=${l}`);
      }
      const enTag = tags.find(t => t[1] === 'en');
      assert(enTag && !enTag[2].includes('?'), 'hreflang en should point at the bare canonical (no query)');
    });
  }

  // ── board SSR: not Loading-only, has city + Example ──
  await step('board SSR HTML contains real content (city tabs + Example cards), not only Loading', async () => {
    const res = await fetch(`${frontendBase}/v2/board/tbilisi`);
    const html = await res.text();
    assert(html.includes('Tbilisi'), 'SSR HTML missing city name Tbilisi');
    assert(/example-badge|Example/.test(html), 'SSR HTML missing Example sample card content');
    assert(!/^[\s\S]*<div class="status-msg">[\s\S]*<\/div>\s*<\/div>\s*<\/body>/.test(html), 'SSR HTML appears to be Loading-only');
  });

  // ── SSR safety: no wallet/contact/token/payment data ──
  await step('board SSR HTML never contains wallet, token, contact, or payment fields', async () => {
    const res = await fetch(`${frontendBase}/v2/board/tbilisi`);
    const html = await res.text();
    for (const term of ['wallet_fingerprint', 'wallet_address', 'management_code', 'recovery_code', 'purchase_token', 'browser_token', 'payment_address', 'contact_ciphertext', 'contact_nonce', 'telegram_chat_id', 'chat_id']) {
      assert(!html.includes(term), `SSR HTML must not contain ${term}`);
    }
  });

  // ── JSON-LD valid ──
  for (const path of ['/v2/how-it-works', '/v2/board/tbilisi']) {
    await step(`${path}: JSON-LD present and valid`, async () => {
      const res = await fetch(`${frontendBase}${path}`);
      const html = await res.text();
      const blocks = [...html.matchAll(/<script type="application\/ld\+json">([\s\S]*?)<\/script>/g)];
      assert(blocks.length > 0, `${path} missing JSON-LD`);
      for (const b of blocks) {
        const obj = JSON.parse(b[1]); // throws on invalid JSON
        assert(obj['@context'] === 'https://schema.org', `${path} JSON-LD missing schema.org context`);
        assert(!['Organization', 'MedicalOrganization', 'AggregateRating', 'Product', 'FAQPage'].includes(obj['@type']), `${path} JSON-LD must not use ${obj['@type']}`);
      }
    });
  }

  // ── SSR <html lang> (point 9) ──
  await step('/v2/how-it-works and /v2/board/tbilisi: SSR <html lang> matches ?lang, fallback en', async () => {
    for (const lang of LANGS) {
      for (const path of ['/v2/how-it-works', '/v2/board/tbilisi']) {
        const res = await fetch(`${frontendBase}${path}?lang=${lang}`);
        const html = await res.text();
        assert(html.includes(`<html lang="${lang}"`), `${path}?lang=${lang}: <html lang> does not match, got: ${(html.match(/<html[^>]*>/) || [''])[0]}`);
      }
    }
    // No ?lang= at all → fallback 'en'.
    for (const path of ['/v2/how-it-works', '/v2/board/tbilisi']) {
      const res = await fetch(`${frontendBase}${path}`);
      const html = await res.text();
      assert(html.includes('<html lang="en"'), `${path} without ?lang must fall back to lang="en"`);
    }
  });
  await step('private pages keep the safe lang="en" fallback', async () => {
    const res = await fetch(`${frontendBase}/v2/new`);
    const html = await res.text();
    assert(html.includes('<html lang="en"'), '/v2/new must render lang="en"');
  });

  // ── EN/RU/ES/KA: no $$, no raw {placeholder}, no stale wording ──
  for (const lang of LANGS) {
    for (const path of ['/v2/how-it-works', '/v2/board/tbilisi']) {
      await step(`${path}?lang=${lang}: no $$, no raw placeholders, no stale wording`, async () => {
        const res = await fetch(`${frontendBase}${path}?lang=${lang}`);
        const html = await res.text();
        assert(!html.includes('$$'), `${path}?lang=${lang} contains $$`);
        assert(!/\{(min|city|msg|n|pos|neg|floor|post_min|balance|required)\}/.test(html), `${path}?lang=${lang} contains an unsubstituted placeholder`);
        for (const term of STALE_TERMS) {
          assert(!term.re.test(html), `${path}?lang=${lang} contains stale term: ${term.name}`);
        }
      });
    }
  }

  // ── Language URL authority after hydration + switcher + cross-page persistence (point 2) ──
  await step('?lang=ru stays authoritative after hydration even when localStorage prefers es', async () => {
    const page = await browser.newPage();
    try {
      await page.addInitScript(() => { try { localStorage.setItem('naroom_lang', 'es'); } catch {} });
      await page.goto(`${frontendBase}/v2/how-it-works?lang=ru`, { waitUntil: 'networkidle' });
      // Give hydration + the layout's initLang()/onMount a moment to run, so
      // this genuinely proves the URL wins over localStorage post-hydration,
      // not just during the initial SSR paint.
      await page.waitForTimeout(500);
      const canonical = await page.locator('link[rel="canonical"]').getAttribute('href');
      assert(canonical && canonical.includes('?lang=ru'), `canonical must still carry ?lang=ru after hydration, got ${canonical}`);
      const bodyText = await page.locator('.content > p').first().textContent();
      assert(/каннабис/i.test(bodyText || ''), `visible text must remain Russian after hydration despite localStorage=es, got: ${bodyText}`);
    } finally {
      await page.close();
    }
  });

  await step('language switcher on /v2/how-it-works updates the URL (en removes ?lang, ru/es/ka add it)', async () => {
    const page = await browser.newPage();
    try {
      await page.goto(`${frontendBase}/v2/how-it-works`, { waitUntil: 'networkidle' });
      await page.click('button.lang-btn:has-text("RU")');
      await page.waitForURL('**/v2/how-it-works?lang=ru');
      await page.click('button.lang-btn:has-text("EN")');
      await page.waitForURL((u) => u.pathname === '/v2/how-it-works' && !u.search);
      const url = new URL(page.url());
      assert(!url.search, `switching to EN must remove ?lang, got ${url.search}`);
    } finally {
      await page.close();
    }
  });

  await step('navigating board → city tab → how-it-works preserves ?lang=ru', async () => {
    const page = await browser.newPage();
    try {
      await page.goto(`${frontendBase}/v2/board/tbilisi?lang=ru`, { waitUntil: 'networkidle' });
      await page.click('a.tab:has-text("Batumi")');
      await page.waitForURL('**/v2/board/batumi?lang=ru');
      await page.click('nav >> text=Как это работает');
      await page.waitForURL('**/v2/how-it-works?lang=ru');
    } finally {
      await page.close();
    }
  });

  // ── Browser layout sanity: 1440x900 and 390x844, EN/RU/ES/KA, how-it-works + board ──
  for (const viewport of [{ w: 1440, h: 900 }, { w: 390, h: 844 }]) {
    for (const lang of LANGS) {
      await step(`layout ${viewport.w}x${viewport.h} lang=${lang}: no horizontal overflow on how-it-works/board`, async () => {
        const page = await browser.newPage({ viewport: { width: viewport.w, height: viewport.h } });
        try {
          for (const path of [`/v2/how-it-works?lang=${lang}`, `/v2/board/tbilisi?lang=${lang}`]) {
            await page.goto(`${frontendBase}${path}`, { waitUntil: 'networkidle' });
            const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1);
            assert(!overflow, `horizontal overflow on ${path} at ${viewport.w}x${viewport.h}`);
          }
        } finally {
          await page.close();
        }
      });
    }
  }

  await browser.close();
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
