// 018_balance_threshold.js — balance gate enforces slot cost ($1000/slot) in prod mode (RS-5)
// devMode=false so the balance check in respond.go is active.
// Wallet sessions are injected directly (registerDirect) to bypass the blockchain API.
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_A = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const CLIENT_B = '1CounterpartyXXXXXXXXXXXXXXXUWLpVr';
const PEER     = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

export async function run() {
  console.log('\n=== 018: Balance Threshold (RS-5) ===');
  // devMode=false enables the balance/slot-cost check in respond.go
  const srv = new TestServer({ devMode: false });
  const t = new Runner('018_balance_threshold');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    // Inject wallet sessions directly — avoids real blockchain API calls in prod mode.
    const tokenA    = srv.registerDirect(CLIENT_A, 'client');
    const tokenB    = srv.registerDirect(CLIENT_B, 'client');
    const tokenPeer = srv.registerDirect(PEER,     'peer', 'BTC', 1000);
    api.tokens[CLIENT_A] = { token: tokenA,    role: 'client' };
    api.tokens[CLIENT_B] = { token: tokenB,    role: 'client' };
    api.tokens[PEER]     = { token: tokenPeer, role: 'peer'   };

    let listingA, listingB;
    const future = Math.floor(Date.now() / 1000) + 3600;

    await t.run('create listing A (client A)', async () => {
      // In prod mode listing/create still issues an invoice — force-activate via DB.
      const r = await api.post('/listing/create', {
        city: 'tbilisi', dependency_type: 'alcohol', help_type: 'crisis',
        urgency: 'urgent', languages: ['en'], currency: 'BTC',
      }, CLIENT_A);
      assertStatus(r, 201, 'createListing A');
      listingA = r.body.listing_id;
      srv.db(`UPDATE listings SET status='active', visible_until=${future} WHERE id='${listingA}'`);
    });

    await t.run('create listing B (client B)', async () => {
      const r = await api.post('/listing/create', {
        city: 'tbilisi', dependency_type: 'alcohol', help_type: 'crisis',
        urgency: 'urgent', languages: ['en'], currency: 'BTC',
      }, CLIENT_B);
      assertStatus(r, 201, 'createListing B');
      listingB = r.body.listing_id;
      srv.db(`UPDATE listings SET status='active', visible_until=${future} WHERE id='${listingB}'`);
    });

    await t.run('peer starts with min_required_usd=1000 (verified on register)', async () => {
      const minReq = srv.db(`SELECT min_required_usd FROM wallet_sessions WHERE role='peer'`);
      if (parseFloat(minReq) !== 1000) throw new Error(`expected min_required_usd=1000, got ${minReq}`);
    });

    await t.run('peer responds to listing A (slot 1: $1000 needed, $1000 have) → 201', async () => {
      const r = await api.respond(listingA, PEER, newKeypair().pub);
      assertStatus(r, 201, 'respond slot 1');
    });

    await t.run('peer responds to listing B (slot 2: $2000 needed, $1000 have) → 403', async () => {
      const r = await api.respond(listingB, PEER, newKeypair().pub);
      if (r.status !== 403) throw new Error(`expected 403 balance gate, got ${r.status}: ${JSON.stringify(r.body)}`);
    });

    await t.run('after injecting $2000 balance, peer can respond to listing B → 201', async () => {
      // Raise the min_required_usd to $2000 (simulates a verified higher balance)
      srv.db(`UPDATE wallet_sessions SET min_required_usd=2000 WHERE role='peer'`);
      const r = await api.respond(listingB, PEER, newKeypair().pub);
      assertStatus(r, 201, 'respond slot 2 after balance raise');
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
