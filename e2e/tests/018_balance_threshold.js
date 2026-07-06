// 018_balance_threshold.js — balance gate enforces slot formula (floor(balance/1000)*2, min 2) in prod mode (RS-5)
// devMode=false so the balance check in respond.go is active.
// Wallet sessions are injected directly (registerDirect) to bypass the blockchain API.
// Listings are injected directly into the DB (no HTTP) to avoid external price API calls.
//
// Slot formula: floor(min_required_usd / 1000) * 2, minimum 2.
//   $1000 → floor(1) * 2 = 2 slots
//   $2000 → floor(2) * 2 = 4 slots
//
// Test strategy:
//   1. Peer at $1000 can fill slot 1 and slot 2 (2 slots available) — both → 201
//   2. After injecting a 2nd active response via DB, 3rd listing → 403 (slots full)
//   3. Raise balance to $2000 → maxSlots becomes 4 → 3rd listing now → 201
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

// injectListing bypasses /listing/create (which requires external price API in devMode=false).
let _seq018 = 0;
function injectListing(srv, city = 'tbilisi') {
  const now = Math.floor(Date.now() / 1000);
  const id = `lst_018_${now}_${++_seq018}`;
  srv.db(
    `INSERT INTO listings (id, city, dependency_type, help_type, urgency, languages, wallet_hash, visible_until, created_at, status, is_sample) ` +
    `VALUES ('${id}', '${city}', 'alcohol', 'crisis', 'urgent', '["en"]', 'fake_hash_018', ${now + 3600}, ${now}, 'active', 0)`
  );
  return id;
}

const CLIENT_A = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const CLIENT_B = '1CounterpartyXXXXXXXXXXXXXXXUWLpVr';
const CLIENT_C = '1EHNa6Q4Jz2uvNExL497mE43ikXhwF6kZm';
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
    const tokenC    = srv.registerDirect(CLIENT_C, 'client');
    const tokenPeer = srv.registerDirect(PEER,     'peer', 'BTC', 1000);
    api.tokens[CLIENT_A] = { token: tokenA,    role: 'client' };
    api.tokens[CLIENT_B] = { token: tokenB,    role: 'client' };
    api.tokens[CLIENT_C] = { token: tokenC,    role: 'client' };
    api.tokens[PEER]     = { token: tokenPeer, role: 'peer'   };

    // Inject listings directly — bypasses external price API call in devMode=false.
    const listingA = injectListing(srv);
    const listingB = injectListing(srv);
    const listingC = injectListing(srv);

    await t.run('peer starts with min_required_usd=1000 (verified on register)', async () => {
      const minReq = srv.db(`SELECT min_required_usd FROM wallet_sessions WHERE role='peer'`);
      if (parseFloat(minReq) !== 1000) throw new Error(`expected min_required_usd=1000, got ${minReq}`);
    });

    await t.run('peer responds to listing A ($1000 balance → 2 slots, slot 1) → 201', async () => {
      const r = await api.respond(listingA, PEER, newKeypair().pub);
      assertStatus(r, 201, 'respond slot 1');
    });

    // At $1000: maxSlots=2, activeResponses=1 → slot 2 should succeed
    await t.run('peer responds to listing B ($1000 balance → 2 slots, slot 2) → 201', async () => {
      const r = await api.respond(listingB, PEER, newKeypair().pub);
      assertStatus(r, 201, 'respond slot 2 (2 slots at $1000)');
    });

    // Now activeResponses=2, maxSlots=2 → listing C must be rejected (slots full)
    await t.run('peer responds to listing C (slots full: 2/2 at $1000) → 403', async () => {
      const r = await api.respond(listingC, PEER, newKeypair().pub);
      if (r.status !== 403) throw new Error(`expected 403 balance gate, got ${r.status}: ${JSON.stringify(r.body)}`);
    });

    // Raise balance to $2000 → maxSlots=4 → listing C should now succeed
    await t.run('after injecting $2000 balance (maxSlots→4), peer can respond to listing C → 201', async () => {
      // Raise the min_required_usd to $2000 (simulates a verified higher balance).
      // Wait for rate-limit bucket to refill (rlRespond = 3/min burst 3; 3 calls already made).
      await new Promise(r => setTimeout(r, 22000));
      srv.db(`UPDATE wallet_sessions SET min_required_usd=2000 WHERE role='peer'`);
      const r = await api.respond(listingC, PEER, newKeypair().pub);
      assertStatus(r, 201, 'respond slot 3 after balance raise to $2000');
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
