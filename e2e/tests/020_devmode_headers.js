// 020_devmode_headers.js — X-Dev-* headers rejected in production mode (SE-4)
// devMode=false: X-Dev-Wallet + X-Dev-Role headers must NOT grant access.
// Only a valid Bearer token (from registerDirect) should work.
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { assertStatus } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const PEER_WALLET = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

export async function run() {
  console.log('\n=== 020: DevMode Headers Rejected in Prod (SE-4) ===');
  const srv = new TestServer({ devMode: false });
  const t = new Runner('020_devmode_headers');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    // Inject a peer session directly (bypasses blockchain API)
    const token = srv.registerDirect(PEER_WALLET, 'peer', 'BTC', 1000);
    api.tokens[PEER_WALLET] = { token, role: 'peer' };

    await t.run('X-Dev-Wallet + X-Dev-Role headers without Bearer token → 401', async () => {
      const r = await fetch(`${srv.base}/peer/region`, {
        method: 'GET',
        headers: {
          'X-Dev-Wallet': PEER_WALLET,
          'X-Dev-Role': 'peer',
        },
      });
      if (r.status !== 401) throw new Error(`expected 401, got ${r.status}`);
    });

    await t.run('X-Dev-* headers combined with valid Bearer token → 200 (Bearer wins)', async () => {
      const r = await fetch(`${srv.base}/peer/region`, {
        method: 'GET',
        headers: {
          'Authorization': `Bearer ${token}`,
          'X-Dev-Wallet': PEER_WALLET,
          'X-Dev-Role': 'peer',
        },
      });
      // Bearer token is valid so request should succeed
      if (r.status !== 200) throw new Error(`expected 200, got ${r.status}`);
    });

    await t.run('valid Bearer token alone (no X-Dev-* headers) → 200', async () => {
      const r = await api.get('/peer/region', PEER_WALLET);
      assertStatus(r, 200, 'Bearer token GET /peer/region');
    });

    await t.run('no headers at all → 401', async () => {
      const r = await fetch(`${srv.base}/peer/region`, { method: 'GET' });
      if (r.status !== 401) throw new Error(`expected 401, got ${r.status}`);
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
