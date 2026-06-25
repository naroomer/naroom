// 015_region_lock.js — peer's region is locked after first response; cross-city responses rejected (RS-4)
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_A = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const CLIENT_B = '1CounterpartyXXXXXXXXXXXXXXXUWLpVr';
const PEER     = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

export async function run() {
  console.log('\n=== 015: Region Lock (RS-4) ===');
  const srv = new TestServer();
  const t = new Runner('015_region_lock');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    await api.verifyWallet(CLIENT_A, 'BTC', 'client');
    await api.verifyWallet(CLIENT_B, 'BTC', 'client');
    await api.verifyWallet(PEER, 'BTC', 'peer');

    let listingA;

    await t.run('client A creates listing in tbilisi', async () => {
      const r = await api.post('/listing/create', {
        city: 'tbilisi', dependency_type: 'alcohol', help_type: 'crisis',
        urgency: 'urgent', languages: ['en'], currency: 'BTC',
      }, CLIENT_A);
      assertStatus(r, 201, 'create listing in tbilisi');
      listingA = r.body.listing_id;
    });

    await t.run('listing A becomes active', async () => {
      await pollUntil(async () => {
        const r = await api.getListing(listingA);
        return r.body.status === 'active' ? true : null;
      }, { timeout: 30000, label: 'listing A active' });
    });

    await t.run('peer responds to tbilisi listing — succeeds, locks region to tbilisi', async () => {
      const peerKeys = newKeypair();
      const r = await api.respond(listingA, PEER, peerKeys.pub);
      assertStatus(r, 201, 'respond to tbilisi listing');
    });

    await t.run('peer GET /peer/region returns tbilisi', async () => {
      const r = await api.get('/peer/region', PEER);
      assertStatus(r, 200, 'GET /peer/region');
      if (r.body.region !== 'tbilisi') throw new Error(`expected region=tbilisi, got ${r.body.region}`);
    });

    // Create a listing in a different city
    await t.run('client B creates listing in batumi', async () => {
      const r = await api.post('/listing/create', {
        city: 'batumi', dependency_type: 'alcohol', help_type: 'crisis',
        urgency: 'urgent', languages: ['en'], currency: 'BTC',
      }, CLIENT_B);
      assertStatus(r, 201, 'create listing in batumi');
      const listingB = r.body.listing_id;

      await pollUntil(async () => {
        const r2 = await api.getListing(listingB);
        return r2.body.status === 'active' ? true : null;
      }, { timeout: 30000, label: 'listing B active' });

      // Peer (locked to tbilisi) tries to respond to batumi listing — must be rejected
      const peerKeys = newKeypair();
      const resp = await api.respond(listingB, PEER, peerKeys.pub);
      if (resp.status !== 403) throw new Error(`expected 403 region_locked, got ${resp.status}: ${JSON.stringify(resp.body)}`);
      if (resp.body.error !== 'region_locked') throw new Error(`expected error=region_locked, got ${JSON.stringify(resp.body)}`);
      if (resp.body.locked_region !== 'tbilisi') throw new Error(`expected locked_region=tbilisi, got ${resp.body.locked_region}`);
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
