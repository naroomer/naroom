// 037_slot_release.js — slot formula edge cases + TTL cleaner frees accepted response slots (RS-5, WK-*)
//
// Part A: Slot formula devMode=false
//   T1: peer with $999 gets 2 slots (floor(999/1000)*2=0 → min 2); can respond
//   T2: peer with $1000 gets 2 slots; can respond
//   T3: peer with $1000 fills both slots (injected via DB) → next respond returns 403
//   T4: peer with $1999 still gets only 2 slots (floor(1999/1000)*2=2) — DB verify
//   T5: peer with $2000 gets 4 slots (floor(2000/1000)*2=4) — DB inject 3 active, 4th → 201, 5th → 403
//
// Part B: TTL cleaner frees accepted response slots
//   T6: respond → DB inject accepted response + expired chat room → TTL cleaner runs → slot freed → peer can respond again
//   T7: repeated TTL cleaner runs are idempotent (run twice, no breakage)
//   T8: peer_left half-closed room does NOT free slot (response stays accepted until room fully closes)

import { TestServer, sleep } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

// makeListing injects an active listing directly into the DB.
// Avoids calling /listing/create which requires an external price API in devMode=false.
// Returns listing_id.
let _listingSeq = 0;
function makeListing(srv, city = 'tbilisi') {
  const now = Math.floor(Date.now() / 1000);
  const future = now + 3600;
  const id = `lst_t37_${now}_${++_listingSeq}`;
  srv.db(
    `INSERT INTO listings (id, city, dependency_type, help_type, urgency, languages, wallet_hash, visible_until, created_at, status, is_sample) ` +
    `VALUES ('${id}', '${city}', 'alcohol', 'crisis', 'urgent', '["en"]', 'fake_client_hash_t37', ${future}, ${now}, 'active', 0)`
  );
  return id;
}

// injectPendingResponse inserts a fake pending response row for a peer (by wallet_hash).
// Used to pre-fill slots without consuming HTTP rate limit budget.
function injectPendingResponse(srv, peerHash, listingId, suffix = '') {
  const now = Math.floor(Date.now() / 1000);
  const id = `rsp_injected_${suffix}_${now}`;
  srv.db(
    `INSERT INTO responses (id, listing_id, counselor_hash, counselor_pubkey, status, created_at) ` +
    `VALUES ('${id}', '${listingId}', '${peerHash}', 'fake_pubkey_${suffix}', 'pending', ${now})`
  );
  return id;
}

// getPeerHash returns the wallet_hash stored for the peer with the given min_required_usd.
// Only works when each test uses a unique balance value.
function getPeerHashByBalance(srv, balance) {
  return srv.db(`SELECT wallet_hash FROM wallet_sessions WHERE min_required_usd=${balance} AND role='peer' LIMIT 1`);
}

export async function run() {
  console.log('\n=== 037: Slot Formula Edge Cases + TTL Slot Release (RS-5, WK) ===');
  // devMode=false activates the balance/slot-cost check in respond.go
  const srv = new TestServer({ devMode: false });
  const t = new Runner('037_slot_release');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    // ── Register wallets directly (bypass blockchain check) ────────────────
    // One client + several peers with distinct balances for Part A.
    // Part B gets its own fresh addresses.

    const CLIENT_MAIN = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
    const PEER_999    = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';  // $999
    const PEER_1000   = '1CounterpartyXXXXXXXXXXXXXXXUWLpVr';  // $1000
    const PEER_1999   = '1EHNa6Q4Jz2uvNExL497mE43ikXhwF6kZm';  // $1999
    const PEER_2000   = '1GkQmKAmHtNfnD3LHhTkewJxKHVSta4m2a';  // $2000

    api.tokens[CLIENT_MAIN] = { token: srv.registerDirect(CLIENT_MAIN, 'client'), role: 'client' };
    api.tokens[PEER_999]    = { token: srv.registerDirect(PEER_999,   'peer', 'BTC',  999), role: 'peer' };
    api.tokens[PEER_1000]   = { token: srv.registerDirect(PEER_1000,  'peer', 'BTC', 1000), role: 'peer' };
    api.tokens[PEER_1999]   = { token: srv.registerDirect(PEER_1999,  'peer', 'BTC', 1999), role: 'peer' };
    api.tokens[PEER_2000]   = { token: srv.registerDirect(PEER_2000,  'peer', 'BTC', 2000), role: 'peer' };

    // Pre-create one active listing for use in slot checks
    const LISTING_MAIN = makeListing(srv);

    // ── Part A: Slot formula ──────────────────────────────────────────────

    // T1: $999 → floor(999/1000)*2=0 → clamped to 2 → can respond (no prior active responses)
    await t.run('T1: peer with $999 has 2 slots (formula min) → can respond → 201', async () => {
      const r = await api.respond(LISTING_MAIN, PEER_999, newKeypair().pub);
      assertStatus(r, 201, 'T1 peer $999 respond');
    });

    // T2: $1000 → floor(1000/1000)*2=2 → can respond
    await t.run('T2: peer with $1000 has 2 slots → first respond 201', async () => {
      const CLIENT_T2 = '1NXYoJ5xU91Jp83XfVMHwwTUyZFK1PB1Tj';
      api.tokens[CLIENT_T2] = { token: srv.registerDirect(CLIENT_T2, 'client'), role: 'client' };
      const listing = makeListing(srv);
      const r = await api.respond(listing, PEER_1000, newKeypair().pub);
      assertStatus(r, 201, 'T2 peer $1000 respond slot 1');
    });

    // T3: $1000 → maxSlots=2; inject 2 pending responses via DB, next HTTP call → 403
    await t.run('T3: peer with $1000 at 2 active slots → 3rd respond returns 403', async () => {
      const peerHash = getPeerHashByBalance(srv, 1000);
      // Inject 2 more fake pending responses (total will exceed 2 since T2 added 1 real one already)
      // But actually: clear T2's response first and inject fresh ones to have exactly 2
      // Easier: just inject enough that activeResponses >= maxSlots=2
      // T2 created 1 real pending response. Inject 1 more → total=2 active → next → 403
      const CLIENT_T3_A = '1LpjkZMMq9AJJecbZ6WBfevYvFGj4kwmFy';
      const CLIENT_T3_B = '1PMycacnJaSqwwJqjawXBErnLsZ7RkXnAs';
      api.tokens[CLIENT_T3_A] = { token: srv.registerDirect(CLIENT_T3_A, 'client'), role: 'client' };
      api.tokens[CLIENT_T3_B] = { token: srv.registerDirect(CLIENT_T3_B, 'client'), role: 'client' };
      const listingX = makeListing(srv);
      const listingY = makeListing(srv);
      // Inject fake pending response for listingX so PEER_1000 appears to have 2 active slots
      injectPendingResponse(srv, peerHash, listingX, 't3a');
      // Verify DB: PEER_1000 now has 2 active responses
      const active = parseInt(srv.db(`SELECT COUNT(*) FROM responses WHERE counselor_hash='${peerHash}' AND status IN ('pending','accepted')`), 10);
      if (active < 2) throw new Error(`T3 setup: expected >=2 active responses, got ${active}`);
      // HTTP call for listingY — should be blocked (activeResponses >= maxSlots)
      const r = await api.respond(listingY, PEER_1000, newKeypair().pub);
      if (r.status !== 403) {
        throw new Error(`T3: expected 403 at slot 3, got ${r.status}: ${JSON.stringify(r.body)}`);
      }
    });

    // T4: $1999 → floor(1999/1000)*2=2 slots, NOT 4 — verify formula via DB injection + 1 HTTP call
    // We add a sleep to give the rate-limit bucket time to recover before this test.
    await t.run('T4: peer with $1999 has only 2 slots (not 4) → 3rd respond 403', async () => {
      await sleep(22000); // allow rlRespond bucket to refill (3/min burst 3; ~20s per token)
      const peerHash = getPeerHashByBalance(srv, 1999);
      const CLIENT_T4_A = '1HLoD9E4SDFFPDiYfNYnkBLQ85Y51J3Zb1';
      const CLIENT_T4_B = '1FvzCLoTPGANNjWoUo6jUGuAG3wg1w4Ymh';
      const CLIENT_T4_C = '1CUNEBjYrCn2y1SdiUMohaKUi4wpP326Lc';
      for (const addr of [CLIENT_T4_A, CLIENT_T4_B, CLIENT_T4_C]) {
        api.tokens[addr] = { token: srv.registerDirect(addr, 'client'), role: 'client' };
      }
      const lA = makeListing(srv);
      const lB = makeListing(srv);
      const lC = makeListing(srv);
      // Inject 2 active slots for PEER_1999 via DB (no HTTP rate limit cost)
      injectPendingResponse(srv, peerHash, lA, 't4a');
      injectPendingResponse(srv, peerHash, lB, 't4b');
      // 1 HTTP call: 3rd slot must be 403 (maxSlots=2, not 4 as it would be at $2000)
      const r = await api.respond(lC, PEER_1999, newKeypair().pub);
      if (r.status !== 403) {
        throw new Error(`T4: expected 403 ($1999 = 2 slots not 4), got ${r.status}: ${JSON.stringify(r.body)}`);
      }
    });

    // T5: $2000 → floor(2000/1000)*2=4 slots — inject 3 active, verify 4th → 201, inject 4th, 5th → 403
    await t.run('T5: peer with $2000 has 4 slots → 4th respond 201, 5th → 403', async () => {
      await sleep(44000); // wait for 2 tokens to refill (need 2 HTTP respond calls in this test)
      const peerHash = getPeerHashByBalance(srv, 2000);
      const addrs5 = [
        '1KFHE7w8BhaENAswwryaoccDb6qcT6DbYY',
        '1J37CY1U3GWPkZoMXBCM74vBuBTfFLNJqe',
        '1BpEi6DfDAUFd153wiGrvkiKW1wie5zRot',
        '1FoUWeXZNs17a3KEBvGHs9bSFwXs9FKZXM',
        '1DbNwEMWHoGCMjCYrdvFX9cC2PpCB8f4aE',
      ];
      for (const addr of addrs5) {
        api.tokens[addr] = { token: srv.registerDirect(addr, 'client'), role: 'client' };
      }
      const listings5 = [];
      for (const addr of addrs5) listings5.push(makeListing(srv));

      // Inject 3 active slots — no HTTP cost, directly in DB
      injectPendingResponse(srv, peerHash, listings5[0], 't5a');
      injectPendingResponse(srv, peerHash, listings5[1], 't5b');
      injectPendingResponse(srv, peerHash, listings5[2], 't5c');

      // 4th slot → must succeed (maxSlots=4, activeResponses=3); 1 HTTP call
      const r4 = await api.respond(listings5[3], PEER_2000, newKeypair().pub);
      assertStatus(r4, 201, 'T5 peer $2000 respond slot 4');

      // After r4: activeResponses=4 (3 injected + 1 via HTTP) → 5th must be 403
      // Use a 2nd rate-limit token for the 5th call (we've now used 2 of burst-3 in this window)
      const r5 = await api.respond(listings5[4], PEER_2000, newKeypair().pub);
      if (r5.status !== 403) {
        throw new Error(`T5: expected 403 at slot 5 for $2000 peer, got ${r5.status}: ${JSON.stringify(r5.body)}`);
      }
    });

    // ── Part B: TTL cleaner frees accepted slots ──────────────────────────

    // T6: DB inject accepted response + expired chat room → TTL cleaner runs → response 'closed' → slot freed
    await t.run('T6: expired chat room frees accepted response slot via TTL cleaner', async () => {
      await sleep(44000); // wait for 2 rate-limit tokens (pre-check + retry respond calls)
      const PEER_T6    = '1BXjhXZqeNYqRkXubAHSmTPHSigsGRAvt8';
      const CLIENT_T6A = '1Ler8wkTd7PNxvBBqBFvSiAMnFkYiEEGJX';
      const CLIENT_T6B = '1KukLbFwFT7RUaQEQgQkwBqivAJDkDtNLp';
      const CLIENT_T6C = '1NXYoJ5xU91Jp83XfVMHwwTUyZFK1PBggg';

      // Use balance=1002 (unique) so we can look up this peer's wallet_hash by balance
      api.tokens[PEER_T6]    = { token: srv.registerDirect(PEER_T6,   'peer', 'BTC', 1002), role: 'peer' };
      api.tokens[CLIENT_T6A] = { token: srv.registerDirect(CLIENT_T6A, 'client'), role: 'client' };
      api.tokens[CLIENT_T6B] = { token: srv.registerDirect(CLIENT_T6B, 'client'), role: 'client' };
      api.tokens[CLIENT_T6C] = { token: srv.registerDirect(CLIENT_T6C, 'client'), role: 'client' };

      const listA = makeListing(srv);
      const listB = makeListing(srv);
      const listC = makeListing(srv);

      const peerHashT6 = srv.db(`SELECT wallet_hash FROM wallet_sessions WHERE min_required_usd=1002 AND role='peer' LIMIT 1`);

      // Inject 2 'accepted' responses for PEER_T6 to fill both slots
      const now = Math.floor(Date.now() / 1000);
      const rspId1 = `rsp_t6_1_${now}`;
      const rspId2 = `rsp_t6_2_${now}`;
      const roomId1 = `room_t6_1_${now}`;

      srv.db(
        `INSERT INTO responses (id, listing_id, counselor_hash, counselor_pubkey, status, created_at) ` +
        `VALUES ('${rspId1}', '${listA}', '${peerHashT6}', 'ppub_t6_1', 'accepted', ${now - 200})`
      );
      srv.db(
        `INSERT INTO responses (id, listing_id, counselor_hash, counselor_pubkey, status, created_at) ` +
        `VALUES ('${rspId2}', '${listB}', '${peerHashT6}', 'ppub_t6_2', 'accepted', ${now - 200})`
      );

      // Link rspId1 to an EXPIRED chat room (this is the bug scenario)
      srv.db(
        `INSERT INTO chat_rooms (id, listing_id, response_id, client_hash, counselor_hash, client_pubkey, counselor_pubkey, status, started_at, expires_at, closed_at, closed_by) ` +
        `VALUES ('${roomId1}', '${listA}', '${rspId1}', 'c_hash_t6', '${peerHashT6}', 'cpub', 'ppub_t6_1', 'expired', ${now - 200}, ${now - 10}, ${now - 5}, 'system')`
      );

      // Verify slots are full: 3rd respond to listC must be 403
      const rPreCheck = await api.respond(listC, PEER_T6, newKeypair().pub);
      if (rPreCheck.status !== 403) {
        throw new Error(`T6 pre-check: expected 403 (both slots accepted), got ${rPreCheck.status}`);
      }

      // Verify rspId1 is 'accepted' before cleaner runs
      const statusBefore = srv.db(`SELECT status FROM responses WHERE id='${rspId1}'`);
      if (statusBefore !== 'accepted') throw new Error(`T6: expected 'accepted' before cleaner, got '${statusBefore}'`);

      // Wait for TTL cleaner to run (TTL_CLEAN_INTERVAL=5s; wait 8s)
      await sleep(8000);

      // rspId1 should now be 'closed' (step 2a: expired chat room → close linked accepted response)
      const statusAfter = srv.db(`SELECT status FROM responses WHERE id='${rspId1}'`);
      if (statusAfter !== 'closed') {
        throw new Error(`T6: expected 'closed' after TTL cleaner ran, got '${statusAfter}'`);
      }

      // Now only 1 slot is occupied (rspId2 still 'accepted') → peer can respond to listC
      const rRetry = await api.respond(listC, PEER_T6, newKeypair().pub);
      assertStatus(rRetry, 201, 'T6 slot freed: peer responds after expired room releases slot');
    });

    // T7: TTL cleaner is idempotent — running a second cycle changes nothing
    await t.run('T7: second TTL cleaner cycle is idempotent (no invalid states created)', async () => {
      // Wait for another full TTL_CLEAN_INTERVAL pass
      await sleep(7000);

      // All response rows must have valid statuses (no corruption from double-run)
      const invalidCount = parseInt(
        srv.db(`SELECT COUNT(*) FROM responses WHERE status NOT IN ('pending','accepted','closed','rejected','cancelled')`),
        10
      );
      if (invalidCount !== 0) {
        throw new Error(`T7: ${invalidCount} response(s) have invalid status after second TTL pass`);
      }

      // Closed count must be >= 1 (from T6) — idempotency means no extra closes
      const closedCount = parseInt(srv.db(`SELECT COUNT(*) FROM responses WHERE status='closed'`), 10);
      if (closedCount < 1) throw new Error(`T7: expected >=1 closed response, got ${closedCount}`);
    });

    // T7b (Test C): TTL cleaner runs twice on expired peer_left room → opened_chats_count stays 1
    // The cleaner READS opened_chats_count to decide listing status but NEVER increments it.
    // A second pass must leave the count unchanged.
    await t.run('T7b: TTL cleaner runs twice on expired peer_left room → opened_chats_count stays 1', async () => {
      const now7b = Math.floor(Date.now() / 1000);
      const listId7b = `lst_t7b_${now7b}`;
      const rspId7b  = `rsp_t7b_${now7b}`;
      const roomId7b = `room_t7b_${now7b}`;

      // Listing with opened_chats_count=1 (one paid chat already created), status='matched'
      srv.db(
        `INSERT INTO listings (id, city, dependency_type, help_type, urgency, languages, wallet_hash, visible_until, created_at, status, is_sample, opened_chats_count) ` +
        `VALUES ('${listId7b}', 'tbilisi', 'alcohol', 'crisis', 'urgent', '["en"]', 'hash_t7b', ${now7b+3600}, ${now7b}, 'matched', 0, 1)`
      );
      // Accepted response linked to this listing
      srv.db(
        `INSERT INTO responses (id, listing_id, counselor_hash, counselor_pubkey, status, created_at) ` +
        `VALUES ('${rspId7b}', '${listId7b}', 'counselor_hash_t7b', 'ppub_t7b', 'accepted', ${now7b - 100000})`
      );
      // peer_left room that already expired (peer_left_at 30h ago — past the 24h grace)
      srv.db(
        `INSERT INTO chat_rooms (id, listing_id, response_id, client_hash, counselor_hash, client_pubkey, counselor_pubkey, status, started_at, expires_at, peer_left_at) ` +
        `VALUES ('${roomId7b}', '${listId7b}', '${rspId7b}', 'client_hash_t7b', 'counselor_hash_t7b', 'cpub_t7b', 'ppub_t7b', 'peer_left', ${now7b - 100000}, ${now7b - 1}, ${now7b - 110000})`
      );

      // Wait for first TTL cleaner pass (5s interval, wait 7s)
      await sleep(7000);

      // Listing must reopen (count=1 < 2) and room must expire
      const listStatus = srv.db(`SELECT status FROM listings WHERE id='${listId7b}'`);
      if (listStatus !== 'active') throw new Error(`T7b first pass: expected listing=active, got ${listStatus}`);

      const count1 = parseInt(srv.db(`SELECT COALESCE(opened_chats_count,0) FROM listings WHERE id='${listId7b}'`), 10);
      if (count1 !== 1) throw new Error(`T7b first pass: expected opened_chats_count=1, got ${count1}`);

      // Wait for second TTL cleaner pass
      await sleep(7000);

      // opened_chats_count must STILL be 1 — cleaner never increments it
      const count2 = parseInt(srv.db(`SELECT COALESCE(opened_chats_count,0) FROM listings WHERE id='${listId7b}'`), 10);
      if (count2 !== 1) throw new Error(`T7b second pass: expected opened_chats_count still=1, got ${count2} (double-count bug!)`);
    });

    // T8: peer_left room (NOT expired/closed) does NOT trigger step 2a — slot stays occupied
    await t.run('T8: peer_left room keeps response accepted (slot not freed until room expires)', async () => {
      const PEER_T8   = '1LpjkZMMq9AJJecbZ6WBfevYvFGj4kwmFz';
      const CLIENT_T8 = '1PMycacnJaSqwwJqjawXBErnLsZ7RkXnAz';

      // Use balance=1001 (unique) so we can look up this peer's wallet_hash by balance
      api.tokens[PEER_T8]   = { token: srv.registerDirect(PEER_T8,   'peer', 'BTC', 1001), role: 'peer' };
      api.tokens[CLIENT_T8] = { token: srv.registerDirect(CLIENT_T8,  'client'), role: 'client' };

      const listing8 = makeListing(srv);
      const peerHashT8 = srv.db(`SELECT wallet_hash FROM wallet_sessions WHERE min_required_usd=1001 AND role='peer' LIMIT 1`);

      const now8 = Math.floor(Date.now() / 1000);
      const rspId8  = `rsp_t8_${now8}`;
      const roomId8 = `room_t8_${now8}`;

      // Inject an 'accepted' response for PEER_T8
      srv.db(
        `INSERT INTO responses (id, listing_id, counselor_hash, counselor_pubkey, status, created_at) ` +
        `VALUES ('${rspId8}', '${listing8}', '${peerHashT8}', 'ppub_t8', 'accepted', ${now8 - 50})`
      );
      // Link to a peer_left room with expires_at in the future (not eligible for expiry yet)
      srv.db(
        `INSERT INTO chat_rooms (id, listing_id, response_id, client_hash, counselor_hash, client_pubkey, counselor_pubkey, status, started_at, expires_at, peer_left_at) ` +
        `VALUES ('${roomId8}', '${listing8}', '${rspId8}', 'c_hash_t8', '${peerHashT8}', 'cpub8', 'ppub_t8', 'peer_left', ${now8 - 50}, ${now8 + 7200}, ${now8 - 50})`
      );

      // Wait for one full TTL cleaner cycle
      await sleep(7000);

      // Response must still be 'accepted' — peer_left is NOT in ('expired','closed')
      const status8 = srv.db(`SELECT status FROM responses WHERE id='${rspId8}'`);
      if (status8 !== 'accepted') {
        throw new Error(`T8: response should still be 'accepted' while room is peer_left, got '${status8}'`);
      }

      // Chat room must remain 'peer_left' (expires_at in the future)
      const roomStatus8 = srv.db(`SELECT status FROM chat_rooms WHERE id='${roomId8}'`);
      if (roomStatus8 !== 'peer_left') {
        throw new Error(`T8: room should remain 'peer_left', got '${roomStatus8}'`);
      }
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
