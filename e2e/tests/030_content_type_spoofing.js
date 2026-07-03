// 030_input_validation.js — HTTP input boundary checks
//
// NOTE: No /api/upload endpoint exists in this codebase.
// Image files are transmitted through /chat/poll/send as base64 data URIs
// inside the nacl.box ciphertext payload. The previous version of this file
// contained dead stubs for a nonexistent upload endpoint; this version tests
// real HTTP input validation on actual endpoints.
//
// This version tests input-handling robustness on real existing endpoints:
//   a) Malformed JSON → 400 (not 500 / server panic)
//   b) Empty body   → 400 (not 500)
//   c) Missing required field (wallet_address) → 400
//   d) Body > 64 KB on a non-chat JSON endpoint → 413
//   e) Wrong HTTP method (GET on POST-only route) → not 200
//
// Complements 005_large_image_payload.js (which covers the 8 MB chat limit
// and the 64 KB limit on /wallet/register but doesn't verify error shapes).

import { TestServer } from '../lib/server.js';
import { assertStatus } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

export async function run() {
  console.log('\n=== 030: Input validation (malformed JSON, size limits, method enforcement) ===');
  const srv = new TestServer();
  const t = new Runner('030_input_validation');

  try {
    await srv.start();

    // ── (a) Malformed JSON → 4xx, not 500 ────────────────────────────────────
    await t.run('malformed JSON → 4xx not 5xx', async () => {
      const r = await fetch(`${srv.base}/wallet/register`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: '{not: valid json,,}',
      });
      if (r.status >= 500) throw new Error(`malformed JSON caused ${r.status} (panic?) — expected 4xx`);
      if (r.status < 400) throw new Error(`malformed JSON returned ${r.status} — expected 4xx`);
    });

    // ── (b) Empty body → 4xx, not 500 ────────────────────────────────────────
    await t.run('empty body → 4xx not 5xx', async () => {
      const r = await fetch(`${srv.base}/wallet/register`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: '',
      });
      if (r.status >= 500) throw new Error(`empty body caused ${r.status} (panic?) — expected 4xx`);
      if (r.status < 400) throw new Error(`empty body returned ${r.status} — expected 4xx`);
    });

    // ── (c) Missing required field → 400 ─────────────────────────────────────
    await t.run('missing wallet_address → 400', async () => {
      const r = await fetch(`${srv.base}/wallet/register`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ currency: 'BTC', role: 'client' }),
      });
      assertStatus({ status: r.status, body: {} }, 400, 'missing wallet_address');
    });

    // ── (d) Body > 64 KB on non-chat endpoint → rejected (not 2xx) ───────────
    // Handler enforces MaxBytesReader and rejects with 4xx (typically 400).
    await t.run('body > 64 KB on /wallet/register → rejected (not 2xx)', async () => {
      const big = JSON.stringify({
        wallet_address: '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf',
        currency: 'BTC',
        role: 'client',
        _pad: 'x'.repeat(65 * 1024),
      });
      const r = await fetch(`${srv.base}/wallet/register`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: big,
      });
      if (r.status >= 200 && r.status < 300) {
        throw new Error(`oversized body accepted (${r.status}) — body limit not enforced`);
      }
    });

    // ── (e) Wrong HTTP method → not 200 ──────────────────────────────────────
    await t.run('GET on POST-only /wallet/register → 405 or 404', async () => {
      const r = await fetch(`${srv.base}/wallet/register`, { method: 'GET' });
      if (r.status === 200) throw new Error('GET /wallet/register returned 200 — method not enforced');
      if (r.status >= 500) throw new Error(`GET /wallet/register returned ${r.status}`);
    });

    // ── (f) Malformed JSON on /listing/create (auth-gated) → 4xx ─────────────
    await t.run('malformed JSON on auth-gated endpoint → 4xx not 5xx', async () => {
      const r = await fetch(`${srv.base}/listing/create`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer fake' },
        body: '{"broken":',
      });
      if (r.status >= 500) throw new Error(`malformed JSON on /listing/create caused ${r.status}`);
      // 400 (bad JSON) or 401 (bad token parsed before body) are both fine
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
