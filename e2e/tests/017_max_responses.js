// 017_max_responses.js — listing capped at 2 pending responses; 3rd peer → 409 (RS-1)
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_A  = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';
const PEER_B  = '1CounterpartyXXXXXXXXXXXXXXXUWLpVr';
const PEER_C  = '1EHNa6Q4Jz2uvNExL497mE43ikXhwF6kZm';

export async function run() {
  console.log('\n=== 017: Max 2 Pending Responses (RS-1) ===');
  const srv = new TestServer();
  const t = new Runner('017_max_responses');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    await api.verifyWallet(CLIENT, 'BTC', 'client');
    await api.verifyWallet(PEER_A, 'BTC', 'peer');
    await api.verifyWallet(PEER_B, 'BTC', 'peer');
    await api.verifyWallet(PEER_C, 'BTC', 'peer');

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

    await t.run('peer A responds → 201', async () => {
      const r = await api.respond(listingId, PEER_A, newKeypair().pub);
      assertStatus(r, 201, 'peer A respond');
    });

    await t.run('peer B responds → 201 (slot 2)', async () => {
      const r = await api.respond(listingId, PEER_B, newKeypair().pub);
      assertStatus(r, 201, 'peer B respond');
    });

    await t.run('peer C responds → 409 (max responses reached)', async () => {
      const r = await api.respond(listingId, PEER_C, newKeypair().pub);
      if (r.status !== 409) throw new Error(`expected 409, got ${r.status}: ${JSON.stringify(r.body)}`);
    });

    await t.run('listing still has exactly 2 pending responses in DB', async () => {
      const count = parseInt(
        srv.db(`SELECT COUNT(*) FROM responses WHERE listing_id='${listingId}' AND status='pending'`), 10,
      );
      if (count !== 2) throw new Error(`expected 2 pending responses, got ${count}`);
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
