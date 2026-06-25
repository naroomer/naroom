// 016_role_separation_respond.js — clients cannot respond to listings (SE-3)
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_A = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const CLIENT_B = '1CounterpartyXXXXXXXXXXXXXXXUWLpVr';

export async function run() {
  console.log('\n=== 016: Client Cannot Respond (SE-3) ===');
  const srv = new TestServer();
  const t = new Runner('016_role_separation_respond');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    await api.verifyWallet(CLIENT_A, 'BTC', 'client');
    await api.verifyWallet(CLIENT_B, 'BTC', 'client');

    let listingId;

    await t.run('client A creates listing', async () => {
      const r = await api.createListing(CLIENT_A);
      assertStatus(r, 201, 'createListing');
      listingId = r.body.listing_id;
    });

    await t.run('listing becomes active', async () => {
      await pollUntil(async () => {
        const r = await api.getListing(listingId);
        return r.body.status === 'active' ? true : null;
      }, { timeout: 30000, label: 'listing active' });
    });

    await t.run('client B (role=client) cannot respond → 403', async () => {
      const keys = newKeypair();
      const r = await api.respond(listingId, CLIENT_B, keys.pub);
      if (r.status !== 403) throw new Error(`expected 403, got ${r.status}: ${JSON.stringify(r.body)}`);
    });

    await t.run('client A (listing owner, role=client) cannot respond to own listing → 403', async () => {
      const keys = newKeypair();
      const r = await api.respond(listingId, CLIENT_A, keys.pub);
      if (r.status !== 403) throw new Error(`expected 403, got ${r.status}: ${JSON.stringify(r.body)}`);
    });

    await t.run('listing is still active (no side-effect from rejected responds)', async () => {
      const r = await api.getListing(listingId);
      if (r.body.status !== 'active') throw new Error(`expected active, got ${r.body.status}`);
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
