// 008_wallet_register.js — wallet registration and principal/session flow
//
// New flow (Fix 4a principal model):
//   1. POST /session/init  → { session_token, recovery_code, expires_in }
//   2. POST /wallet/register (Bearer token) → { status: "ok", wallet_linked: true }
//
// The session_token comes from /session/init, NOT from /wallet/register.
// /wallet/register is idempotent — calling it again with the same wallet is safe.

import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { assertStatus, assertHasField } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_WALLET   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

export async function run() {
  console.log('\n=== 008: Wallet Register (principal model) ===');
  const srv = new TestServer();
  const t = new Runner('008_wallet_register');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    await t.run('POST /session/init (client) issues session_token + recovery_code', async () => {
      const r = await fetch(`${srv.base}/session/init`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ role: 'client' }),
      });
      if (r.status !== 201) throw new Error(`Expected 201, got ${r.status}`);
      const body = await r.json();
      assertHasField(body, 'session_token', '/session/init response');
      assertHasField(body, 'recovery_code', '/session/init response');
      if (body.expires_in !== 86400) throw new Error(`expires_in=${body.expires_in}, expected 86400`);
    });

    await t.run('POST /wallet/register (client) → wallet_linked: true (no session_token)', async () => {
      const r = await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      assertStatus(r, 200, 'register');
      if (!r.body.wallet_linked) throw new Error(`Expected wallet_linked: true, got: ${JSON.stringify(r.body)}`);
      if (r.body.session_token) throw new Error(`/wallet/register must NOT return session_token — use /session/init instead`);
    });

    await t.run('DB: session token stored as hash (not plain)', async () => {
      const tokenHash = srv.db(`SELECT token_hash FROM sessions LIMIT 1`);
      if (tokenHash.length !== 64) throw new Error(`token_hash length=${tokenHash.length}, expected 64`);
    });

    await t.run('DB: principal created and linked to session', async () => {
      const count = srv.db(`SELECT COUNT(*) FROM principals`);
      if (parseInt(count, 10) < 1) throw new Error('No principals in DB after /session/init');
      const linked = srv.db(`SELECT COUNT(*) FROM sessions WHERE principal_id IS NOT NULL`);
      if (parseInt(linked, 10) < 1) throw new Error('No sessions with principal_id');
    });

    await t.run('/wallet/register without session → 401', async () => {
      const r = await fetch(`${srv.base}/wallet/register`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ wallet_address: CLIENT_WALLET, currency: 'BTC', role: 'client' }),
      });
      if (r.status !== 401) throw new Error(`Expected 401 (session required), got ${r.status}`);
    });

    await t.run('missing wallet_address → 400', async () => {
      const token = api.getToken(CLIENT_WALLET);
      const r = await fetch(`${srv.base}/wallet/register`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
        body: JSON.stringify({ currency: 'BTC', role: 'client' }),
      });
      if (r.status !== 400) throw new Error(`Expected 400, got ${r.status}`);
    });

    await t.run('invalid role → 400', async () => {
      const token = api.getToken(CLIENT_WALLET);
      const r = await fetch(`${srv.base}/wallet/register`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
        body: JSON.stringify({ wallet_address: CLIENT_WALLET, currency: 'BTC', role: 'admin' }),
      });
      if (r.status !== 400 && r.status !== 429) throw new Error(`Expected 400 or 429, got ${r.status}`);
    });

    await t.run('invalid currency → 400', async () => {
      const token = api.getToken(CLIENT_WALLET);
      const r = await fetch(`${srv.base}/wallet/register`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
        body: JSON.stringify({ wallet_address: CLIENT_WALLET, currency: 'ETH', role: 'client' }),
      });
      if (r.status !== 400 && r.status !== 429) throw new Error(`Expected 400 or 429, got ${r.status}`);
    });

    await t.run('POST /wallet/register (peer) → wallet_linked: true', async () => {
      const r = await api.verifyWallet(PEER_WALLET, 'BTC', 'peer');
      assertStatus(r, 200, 'register peer');
      if (!r.body.wallet_linked) throw new Error(`Expected wallet_linked: true, got: ${JSON.stringify(r.body)}`);
    });

    await t.run('second verifyWallet for same wallet is idempotent (200 ok)', async () => {
      // delete stored token so verifyWallet creates a fresh session
      delete api.tokens[CLIENT_WALLET];
      const r = await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      assertStatus(r, 200, 're-register');
      if (!r.body.wallet_linked) throw new Error(`Expected wallet_linked: true on re-register`);
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
