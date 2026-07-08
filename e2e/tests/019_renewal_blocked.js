// 019_renewal_blocked.js — renewal blocked when listing.opened_chats_count >= 2 (new entitlement model)
//
// Old model: blocked at 2 pending responses.
// New model: renewal is always free while opened_chats_count < 2.
// At opened_chats_count = 2 (two paid chats created) renewal returns 409.
//
// Tests:
//   - renewal always succeeds at count=0 (no matter how many pending responses)
//   - renewal succeeds at count=1 (first paid chat created, second not yet)
//   - renewal blocked at count=2 → 409
//   - can_renew=false exposed in GET /listing/{id} when count >= 2
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_A  = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';
const PEER_B  = '1CounterpartyXXXXXXXXXXXXXXXUWLpVr';

export async function run() {
  console.log('\n=== 019: Renewal Blocked at opened_chats_count >= 2 (New Entitlement Model) ===');
  const srv = new TestServer({ devMode: true });
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

    await t.run('renewal succeeds when 0 opened chats', async () => {
      const r = await api.post(`/listing/${listingId}/renew`, {}, CLIENT);
      assertStatus(r, 200, 'renew with 0 opened chats');
      if (r.body.status !== 'renewed') throw new Error(`expected status=renewed, got ${r.body.status}`);
      if (r.body.free !== true) throw new Error(`expected free=true, got ${r.body.free}`);
    });

    // Inject opened_chats_count=1 directly (simulates first paid chat opened)
    await t.run('inject opened_chats_count=1, renewal still allowed', async () => {
      srv.db(`UPDATE listings SET opened_chats_count = 1, status = 'active' WHERE id = '${listingId}'`);
      const r = await api.post(`/listing/${listingId}/renew`, {}, CLIENT);
      assertStatus(r, 200, 'renew with opened_chats_count=1');
      if (r.body.free !== true) throw new Error(`expected free=true, got ${r.body.free}`);
    });

    // Inject opened_chats_count=2 directly (simulates second paid chat opened)
    await t.run('inject opened_chats_count=2, renewal blocked → 409', async () => {
      srv.db(`UPDATE listings SET opened_chats_count = 2, status = 'active' WHERE id = '${listingId}'`);
      const r = await api.post(`/listing/${listingId}/renew`, {}, CLIENT);
      if (r.status !== 409) throw new Error(`expected 409 renewal blocked, got ${r.status}: ${JSON.stringify(r.body)}`);
    });

    await t.run('GET /listing/{id} shows can_renew=false when opened_chats_count=2', async () => {
      const r = await api.getListing(listingId);
      if (r.body.can_renew !== false) throw new Error(`expected can_renew=false, got ${r.body.can_renew}`);
      if (r.body.opened_chats_count !== 2) throw new Error(`expected opened_chats_count=2, got ${r.body.opened_chats_count}`);
    });

    await t.run('GET /listing/{id} shows can_renew=true when opened_chats_count=1', async () => {
      srv.db(`UPDATE listings SET opened_chats_count = 1, status = 'active' WHERE id = '${listingId}'`);
      const r = await api.getListing(listingId);
      if (r.body.can_renew !== true) throw new Error(`expected can_renew=true when count=1, got ${r.body.can_renew}`);
    });

    await t.run('GET /listing/{id} shows can_renew=true when opened_chats_count=0', async () => {
      srv.db(`UPDATE listings SET opened_chats_count = 0, status = 'active' WHERE id = '${listingId}'`);
      const r = await api.getListing(listingId);
      if (r.body.can_renew !== true) throw new Error(`expected can_renew=true when count=0, got ${r.body.can_renew}`);
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
