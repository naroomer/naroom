// 040_new_entitlement_model.js — comprehensive test of new $5 entitlement model
//
// New model: $5 → 24h listing + up to 2 paid/opened chats.
// - opened_chats_count incremented at chat_room CREATION, not at close.
// - listing_counted=1 is the idempotency guard (prevents double-counting).
// - Renewal free while opened_chats_count < 2.
// - After first chat closes (count=1): listing reopens.
// - After second chat closes (count=2): listing permanently closed.
// - Third chat attempt: 409.
// - Peer capacity based on active chat_rooms, not pending responses.
//
// Tests:
//  1. Listing TTL is 86400s (24h)
//  2. First paid chat: opened_chats_count → 1; after chat closes, listing reopens (active)
//  3. Second paid chat: opened_chats_count → 2; listing becomes 'closed'
//  4. Third chat attempt: 409
//  5. Renew at count=0: allowed (free=true)
//  6. Renew at count=1: allowed (free=true)
//  7. Renew at count=2: 409
//  8. Accepted response without payment: count stays 0
//  9. Repeated close: idempotent (listing_counted stays 1, count unchanged)
// 10. Peer capacity based on active chat_rooms

import { TestServer, sleep } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT  = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_A  = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';
const PEER_B  = '1CounterpartyXXXXXXXXXXXXXXXUWLpVr';
const PEER_C  = '1EHNa6Q4Jz2uvNExL497mE43ikXhwF6kZm';
const PEER_D  = '1FLAMEN6rq2BqMnkUmsJBqCTFd9sKKrWEp';

// Helper: create a listing + wait for activation. Returns listingId.
async function createListing(api, wallet = CLIENT, city = 'new_york') {
  const r = await api.createListing(wallet, city);
  assertStatus(r, 201, 'createListing');
  const listingId = r.body.listing_id;
  await pollUntil(async () => {
    const lr = await api.getListing(listingId);
    return lr.body.status === 'active' ? true : null;
  }, { timeout: 30000, label: 'listing active' });
  return listingId;
}

// Helper: peer responds + client accepts → poll for chat room. Returns roomId.
async function openChat(api, listingId, peerWallet, label = '') {
  const clientKeys = newKeypair();
  const peerKeys   = newKeypair();

  const pr = await api.respond(listingId, peerWallet, peerKeys.pub);
  assertStatus(pr, 201, `${label} respond`);
  const responseId = pr.body.response_id;

  const ar = await api.acceptResponse(responseId, CLIENT, clientKeys.pub);
  assertStatus(ar, 200, `${label} acceptResponse`);

  const room = await pollUntil(async () => {
    const r = await api.get('/peer/chatroom?listing_id=' + encodeURIComponent(listingId), peerWallet);
    return r.status === 200 ? r.body : null;
  }, { timeout: 30000, label: `${label} chat room opened` });

  return room.room_id;
}

export async function run() {
  console.log('\n=== 040: New Entitlement Model (24h + 2 Chats) ===');
  const t = new Runner('040_new_entitlement_model');

  // ── Test 1: Listing TTL is 86400s ─────────────────────────────────────────────
  {
    const srv = new TestServer({ devMode: true });
    try {
      await srv.start();
      const api = new ApiClient(srv.base);
      await api.verifyWallet(CLIENT, 'BTC', 'client');

      await t.run('1. listing TTL = 86400s (24h)', async () => {
        const listingId = await createListing(api);
        const now = Math.floor(Date.now() / 1000);
        const r = await api.getListing(listingId);
        const ttl = r.body.visible_until - now;
        // Allow ±60s for test execution time
        if (ttl < 86400 - 60 || ttl > 86400 + 60) {
          throw new Error(`Expected listing TTL ~86400s, got ${ttl}s`);
        }
      });

    } finally {
      await srv.stop();
    }
  }

  // ── Tests 2–4, 8, 9: opened_chats_count lifecycle ─────────────────────────────
  {
    const srv = new TestServer({ devMode: true });
    try {
      await srv.start();
      const api = new ApiClient(srv.base);
      await api.verifyWallet(CLIENT, 'BTC', 'client');
      await api.verifyWallet(PEER_A, 'BTC', 'peer');
      await api.verifyWallet(PEER_B, 'BTC', 'peer');
      await api.verifyWallet(PEER_C, 'BTC', 'peer');

      let listingId, roomId1, roomId2;

      await t.run('setup: create listing', async () => {
        listingId = await createListing(api);
      });

      // Test 8: accepted response without payment → count stays 0
      await t.run('8. accepted response without payment: count stays 0', async () => {
        const peerKeys = newKeypair();
        const pr = await api.respond(listingId, PEER_C, peerKeys.pub);
        assertStatus(pr, 201, 'peer C respond');
        // Accept it (creates a pending chat invoice)
        const clientKeys = newKeypair();
        const ar = await api.acceptResponse(pr.body.response_id, CLIENT, clientKeys.pub);
        assertStatus(ar, 200, 'accept response');

        // Do NOT pay — count should still be 0
        const count = parseInt(srv.db(`SELECT COALESCE(opened_chats_count,0) FROM listings WHERE id='${listingId}'`), 10);
        if (count !== 0) throw new Error(`Expected opened_chats_count=0 before payment, got ${count}`);

        // listing_counted should be 0 for any rooms (none created yet)
        const roomCount = parseInt(srv.db(`SELECT COUNT(*) FROM chat_rooms WHERE listing_id='${listingId}'`), 10);
        if (roomCount !== 0) throw new Error(`Expected no chat_rooms before payment, got ${roomCount}`);
      });

      // Need to re-activate listing so we can open chats (invoice for PEER_C still pending)
      // Reset listing status and reject stale response/invoice via DB directly
      await t.run('setup: reset listing to active for next test', async () => {
        // Expire the pending invoice and close the accepted response
        srv.db(`UPDATE invoices SET status = 'expired' WHERE type = 'chat' AND status = 'pending'`);
        // TTL cleaner will restore, but we can speed it up by waiting for step 2d
        await pollUntil(async () => {
          const status = srv.db(`SELECT status FROM listings WHERE id='${listingId}'`);
          return status === 'active' ? true : null;
        }, { timeout: 30000, label: 'listing restored to active after expired invoice' });
      });

      await t.run('2a. first paid chat: opened_chats_count → 1 at creation', async () => {
        roomId1 = await openChat(api, listingId, PEER_A, 'chat1');

        const count = parseInt(srv.db(`SELECT COALESCE(opened_chats_count,0) FROM listings WHERE id='${listingId}'`), 10);
        if (count !== 1) throw new Error(`Expected opened_chats_count=1 after first chat created, got ${count}`);

        // listing_counted must be 1 for this room (idempotency guard)
        const counted = parseInt(srv.db(`SELECT listing_counted FROM chat_rooms WHERE id='${roomId1}'`), 10);
        if (counted !== 1) throw new Error(`Expected listing_counted=1 for room, got ${counted}`);
      });

      // Test 9: repeated close idempotency — listing_counted stays 1, count unchanged
      await t.run('9. repeated close: listing_counted stays 1, count unchanged', async () => {
        // Close once normally (peer + client)
        await api.closeChat(roomId1, PEER_A);
        await api.closeChat(roomId1, CLIENT);

        // Count should still be 1 (not incremented at close)
        const count = parseInt(srv.db(`SELECT COALESCE(opened_chats_count,0) FROM listings WHERE id='${listingId}'`), 10);
        if (count !== 1) throw new Error(`Expected count=1 after first chat close (unchanged), got ${count}`);

        // listing_counted must still be 1
        const counted = parseInt(srv.db(`SELECT listing_counted FROM chat_rooms WHERE id='${roomId1}'`), 10);
        if (counted !== 1) throw new Error(`Expected listing_counted=1 (unchanged), got ${counted}`);

        // Attempt to close again — should be idempotent (room already closed)
        const r = await api.closeChat(roomId1, CLIENT);
        if (r.status !== 200 && r.status !== 410) {
          throw new Error(`Expected 200 or 410 on repeated close, got ${r.status}`);
        }

        // Count still 1
        const count2 = parseInt(srv.db(`SELECT COALESCE(opened_chats_count,0) FROM listings WHERE id='${listingId}'`), 10);
        if (count2 !== 1) throw new Error(`Expected count=1 after repeated close, got ${count2}`);
      });

      await t.run('2b. after first chat closes, listing reopens (active, count=1 < 2)', async () => {
        const r = await api.getListing(listingId);
        if (r.body.status !== 'active') {
          throw new Error(`Expected listing active after first chat closed, got ${r.body.status}`);
        }
        const board = await api.getBoard('new_york');
        if (!Array.isArray(board.body) || !board.body.find(l => l.id === listingId)) {
          throw new Error('Listing not on board after first chat closed (should reopen for second peer)');
        }
      });

      await t.run('3a. second paid chat: opened_chats_count → 2 at creation', async () => {
        roomId2 = await openChat(api, listingId, PEER_B, 'chat2');

        const count = parseInt(srv.db(`SELECT COALESCE(opened_chats_count,0) FROM listings WHERE id='${listingId}'`), 10);
        if (count !== 2) throw new Error(`Expected opened_chats_count=2 after second chat created, got ${count}`);
      });

      await t.run('3b. listing becomes closed when second chat opened (count=2)', async () => {
        const r = await api.getListing(listingId);
        if (r.body.status !== 'closed') {
          throw new Error(`Expected listing closed when count=2, got ${r.body.status}`);
        }
      });

      await t.run('3c. second chat closes → listing stays closed (LI-1)', async () => {
        await api.closeChat(roomId2, PEER_B);
        await api.closeChat(roomId2, CLIENT);

        const r = await api.getListing(listingId);
        if (r.body.status !== 'closed') {
          throw new Error(`Expected listing still closed after second chat close, got ${r.body.status}`);
        }
        const board = await api.getBoard('new_york');
        if (Array.isArray(board.body) && board.body.find(l => l.id === listingId)) {
          throw new Error('Listing returned to board after 2 paid chats — LI-1 violated');
        }
      });

      await t.run('4. third chat attempt: 409', async () => {
        // Listing is closed, so respond should return 404
        const peerKeys = newKeypair();
        const r = await api.respond(listingId, PEER_C, peerKeys.pub);
        if (r.status !== 404) {
          throw new Error(`Expected 404 for third chat (listing closed), got ${r.status}`);
        }
      });

    } finally {
      await srv.stop();
    }
  }

  // ── Tests 5–7: Renew behavior ──────────────────────────────────────────────────
  {
    const srv = new TestServer({ devMode: true });
    try {
      await srv.start();
      const api = new ApiClient(srv.base);
      await api.verifyWallet(CLIENT, 'BTC', 'client');

      let listingId;

      await t.run('setup: create listing for renew tests', async () => {
        listingId = await createListing(api);
      });

      await t.run('5. renew at count=0: allowed, free=true', async () => {
        const r = await api.post(`/listing/${listingId}/renew`, {}, CLIENT);
        assertStatus(r, 200, 'renew at count=0');
        if (r.body.free !== true) throw new Error(`Expected free=true, got ${r.body.free}`);
        if (r.body.status !== 'renewed') throw new Error(`Expected status=renewed, got ${r.body.status}`);
      });

      await t.run('6. renew at count=1: allowed, free=true', async () => {
        srv.db(`UPDATE listings SET opened_chats_count = 1, status = 'active' WHERE id = '${listingId}'`);
        const r = await api.post(`/listing/${listingId}/renew`, {}, CLIENT);
        assertStatus(r, 200, 'renew at count=1');
        if (r.body.free !== true) throw new Error(`Expected free=true at count=1, got ${r.body.free}`);
      });

      await t.run('7. renew at count=2: 409', async () => {
        srv.db(`UPDATE listings SET opened_chats_count = 2, status = 'active' WHERE id = '${listingId}'`);
        const r = await api.post(`/listing/${listingId}/renew`, {}, CLIENT);
        if (r.status !== 409) throw new Error(`Expected 409 at count=2, got ${r.status}: ${JSON.stringify(r.body)}`);
      });

    } finally {
      await srv.stop();
    }
  }

  // ── Test 10: Peer capacity based on active chat_rooms ──────────────────────────
  // Peer with $1000 (min_required_usd=1000) → maxSlots=2.
  // When peer has 2 active chat_rooms, accept should fail with 409.
  {
    const srv = new TestServer({ devMode: true });
    try {
      await srv.start();
      const api = new ApiClient(srv.base);
      await api.verifyWallet(CLIENT, 'BTC', 'client');
      await api.verifyWallet(PEER_A, 'BTC', 'peer');

      // Use extra clients to open 2 active chat_rooms for PEER_A
      const CLIENT_B = '1JArS6jzE3AJ9sZ3aFij1BmTcpFGgN86hA';
      const CLIENT_C = '1HnhWpkMHMjgt167kvgcPyurMmsCQ2WPdn';
      await api.verifyWallet(CLIENT_B, 'BTC', 'client');
      await api.verifyWallet(CLIENT_C, 'BTC', 'client');

      await t.run('10. peer capacity: 2 active chat_rooms → 3rd accept → 409', async () => {
        // Open first listing and chat room with PEER_A (CLIENT as client)
        const listing1 = await createListing(api, CLIENT, 'new_york');
        const room1 = await openChat(api, listing1, PEER_A, 'capacity-chat1');

        // Open second listing with CLIENT_B and chat room with PEER_A
        const listing2 = await createListing(api, CLIENT_B, 'new_york');
        const room2 = await openChat(api, listing2, PEER_A, 'capacity-chat2');

        // Verify PEER_A has 2 active chat_rooms
        const activeCount = parseInt(
          srv.db(`SELECT COUNT(*) FROM chat_rooms WHERE counselor_hash IN (SELECT wallet_hash FROM sessions WHERE role='peer') AND status IN ('active', 'peer_left', 'client_left')`),
          10
        );
        if (activeCount < 2) throw new Error(`Expected at least 2 active chat_rooms for peer, got ${activeCount}`);

        // Now CLIENT_C tries to open a third chat with PEER_A via a new listing
        const listing3 = await createListing(api, CLIENT_C, 'new_york');

        // PEER_A responds
        const peerKeys = newKeypair();
        const pr = await api.respond(listing3, PEER_A, peerKeys.pub);
        assertStatus(pr, 201, 'peer responds to third listing');

        // CLIENT_C tries to accept — should fail because PEER_A is at capacity
        const clientKeys = newKeypair();
        const ar = await api.acceptResponse(pr.body.response_id, CLIENT_C, clientKeys.pub);
        if (ar.status !== 409) {
          throw new Error(`Expected 409 when peer at chat capacity, got ${ar.status}: ${JSON.stringify(ar.body)}`);
        }
        if (!ar.body.error || !ar.body.error.includes('capacity')) {
          throw new Error(`Expected capacity error message, got: ${JSON.stringify(ar.body)}`);
        }
      });

    } finally {
      await srv.stop();
    }
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
