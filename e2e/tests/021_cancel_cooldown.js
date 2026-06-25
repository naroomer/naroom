// 021_cancel_cooldown.js — 30-minute cooldown enforced after peer cancels response (RS-3)
import { TestServer, sleep } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_WALLET   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

export async function run() {
  console.log('\n=== 021: Cancel Cooldown 30min (RS-3) ===');
  const srv = new TestServer({ devMode: true });
  const t = new Runner('021_cancel_cooldown');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
    await api.verifyWallet(PEER_WALLET, 'BTC', 'peer');

    let listingId;
    let responseId;

    await t.run('client creates listing', async () => {
      const r = await api.createListing(CLIENT_WALLET);
      assertStatus(r, 201, 'createListing');
      listingId = r.body.listing_id;
    });

    await t.run('listing becomes active', async () => {
      await pollUntil(async () => {
        const r = await api.getListing(listingId);
        return r.body.status === 'active' ? true : null;
      }, { timeout: 30000, label: 'listing active' });
    });

    await t.run('peer responds → 201', async () => {
      const r = await api.respond(listingId, PEER_WALLET, newKeypair().pub);
      assertStatus(r, 201, 'peer respond');
      responseId = r.body.response_id;
      if (!responseId) throw new Error(`missing response_id in body: ${JSON.stringify(r.body)}`);
    });

    await t.run('peer cancels response → 200', async () => {
      const r = await api.cancelResponse(responseId, PEER_WALLET);
      assertStatus(r, 200, 'cancel response');
    });

    await t.run('peer immediately responds again → 429 (cooldown active)', async () => {
      const r = await api.respond(listingId, PEER_WALLET, newKeypair().pub);
      if (r.status !== 429) throw new Error(`expected 429 cooldown, got ${r.status}: ${JSON.stringify(r.body)}`);
    });

    await t.run('DB: clear cooldown for listing', async () => {
      srv.db(`UPDATE responses SET cooldown_until = 0 WHERE listing_id = '${listingId}'`);
    });

    await t.run('peer responds after cooldown cleared → 201', async () => {
      const r = await api.respond(listingId, PEER_WALLET, newKeypair().pub);
      assertStatus(r, 201, 'respond after cooldown cleared');
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
