// 019_renewal_blocked.js — renewal blocked when listing already has 2 pending responses (LS-3)
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_A  = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';
const PEER_B  = '1CounterpartyXXXXXXXXXXXXXXXUWLpVr';

export async function run() {
  console.log('\n=== 019: Renewal Blocked at 2 Responses (LS-3) ===');
  const srv = new TestServer();
  const t = new Runner('019_renewal_blocked');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    await api.verifyWallet(CLIENT, 'BTC', 'client');
    await api.verifyWallet(PEER_A, 'BTC', 'peer');
    await api.verifyWallet(PEER_B, 'BTC', 'peer');

    let listingId;

    await t.run('client creates listing', async () => {
      const r = await api.createListing(CLIENT);
      assertStatus(r, 201, 'createListing');
      listingId = r.body.listing_id;
    });

    await t.run('listing becomes active', async () => {
      await pollUntil(async () => {
        const r = await api.getListing(listingId);
        return r.body.status === 'active' ? true : null;
      }, { timeout: 30000, label: 'listing active' });
    });

    await t.run('renewal succeeds when 0 responses', async () => {
      const r = await api.post(`/listing/${listingId}/renew`, {}, CLIENT);
      assertStatus(r, 200, 'renew with 0 responses');
      if (r.body.status !== 'renewed') throw new Error(`expected status=renewed, got ${r.body.status}`);
    });

    await t.run('peer A responds (1st slot)', async () => {
      const r = await api.respond(listingId, PEER_A, newKeypair().pub);
      assertStatus(r, 201, 'peer A respond');
    });

    await t.run('renewal succeeds when 1 response', async () => {
      const r = await api.post(`/listing/${listingId}/renew`, {}, CLIENT);
      assertStatus(r, 200, 'renew with 1 response');
    });

    await t.run('peer B responds (2nd slot)', async () => {
      const r = await api.respond(listingId, PEER_B, newKeypair().pub);
      assertStatus(r, 201, 'peer B respond');
    });

    await t.run('renewal blocked at 2 responses → 409', async () => {
      const r = await api.post(`/listing/${listingId}/renew`, {}, CLIENT);
      if (r.status !== 409) throw new Error(`expected 409 renewal blocked, got ${r.status}: ${JSON.stringify(r.body)}`);
    });

    await t.run('GET /listing/{id} shows can_renew=false', async () => {
      const r = await api.getListing(listingId);
      if (r.body.can_renew !== false) throw new Error(`expected can_renew=false, got ${r.body.can_renew}`);
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
