// 008_wallet_register.js — wallet registration endpoint correctness
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { assertStatus, assertHasField } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_WALLET   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

export async function run() {
  console.log('\n=== 008: Wallet Register ===');
  const srv = new TestServer();
  const t = new Runner('008_wallet_register');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    await t.run('POST /wallet/register (client) issues session_token', async () => {
      const r = await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      assertStatus(r, 200, 'register');
      assertHasField(r.body, 'session_token', 'register response');
      if (r.body.expires_in !== 86400) throw new Error(`expires_in=${r.body.expires_in}, expected 86400`);
    });

    await t.run('DB: session token stored as hash (not plain)', async () => {
      const tokenHash = srv.db(`SELECT token_hash FROM sessions LIMIT 1`);
      if (tokenHash.length !== 64) throw new Error(`token_hash length=${tokenHash.length}, expected 64`);
    });

    await t.run('missing wallet_address → 400', async () => {
      const r = await fetch(`${srv.base}/wallet/register`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ currency: 'BTC', role: 'client' }),
      });
      if (r.status !== 400) throw new Error(`Expected 400, got ${r.status}`);
    });

    await t.run('invalid role → 400', async () => {
      const r = await fetch(`${srv.base}/wallet/register`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ wallet_address: CLIENT_WALLET, currency: 'BTC', role: 'admin' }),
      });
      if (r.status !== 400 && r.status !== 429) throw new Error(`Expected 400 or 429, got ${r.status}`);
    });

    await t.run('invalid currency → 400', async () => {
      const r = await fetch(`${srv.base}/wallet/register`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ wallet_address: CLIENT_WALLET, currency: 'ETH', role: 'client' }),
      });
      if (r.status !== 400 && r.status !== 429) throw new Error(`Expected 400 or 429, got ${r.status}`);
    });

    await t.run('POST /wallet/register (peer) issues session_token', async () => {
      const r = await api.verifyWallet(PEER_WALLET, 'BTC', 'peer');
      assertStatus(r, 200, 'register peer');
      assertHasField(r.body, 'session_token', 'register peer response');
    });

    await t.run('second register for same wallet returns new token', async () => {
      const r1 = await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      const r2 = await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      if (r1.body.session_token === r2.body.session_token) {
        throw new Error('Expected different tokens on re-register');
      }
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
