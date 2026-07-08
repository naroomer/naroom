// 039_listing_completed.js — listing lifecycle after paid chat (new entitlement model)
//
// New model: $5 → 24h + up to 2 paid chats. opened_chats_count tracks how many.
// - After 1st paid chat closes: listing reopens (active) if count < 2.
// - After 2nd paid chat closes: listing permanently closed (count >= 2).
// - T3: accepted response + chat invoice expires (no payment) → listing returns to 'active'.
// - T5: TTL expires half-closed room. If count < 2 → listing reopens.
//
// Product invariants:
//   LI-1: once opened_chats_count >= 2, listing never returns to board.
//   LI-2: first paid chat close (count=1) reopens listing for a second peer.
//
// Tests:
//   T1: 2 paid chats created → both close → listing permanently closed (LI-1)
//   T2: after 2nd chat closes → peer cannot respond again (404)
//   T3: accepted response + chat invoice expires (no payment) → listing returns to 'active', peer slot freed
//   T4: first paid chat closes (count=1) → listing reopens (active); second closes (count=2) → listing closed
//   T5: peer closes, TTL expires half-closed room at count=1 → listing reopens (active)

import { TestServer, sleep } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_WALLET   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';
const PEER2_WALLET  = '1CounterpartyXXXXXXXXXXXXXXXUWLpVr';
const PEER3_WALLET  = '1EHNa6Q4Jz2uvNExL497mE43ikXhwF6kZm';

// Full flow helper: opens one paid chat for an existing listing.
// Requires listing to be in 'active' status.
// Returns { roomId }.
async function openPaidChatForListing(api, listingId, peerWallet, label = '') {
  const clientKeys = newKeypair();
  const peerKeys   = newKeypair();

  const pr = await api.respond(listingId, peerWallet, peerKeys.pub);
  assertStatus(pr, 201, `${label} respond`);
  const responseId = pr.body.response_id;

  const ar = await api.acceptResponse(responseId, CLIENT_WALLET, clientKeys.pub);
  assertStatus(ar, 200, `${label} acceptResponse`);

  const room = await pollUntil(async () => {
    const r = await api.get('/peer/chatroom?listing_id=' + encodeURIComponent(listingId), peerWallet);
    return r.status === 200 ? r.body : null;
  }, { timeout: 30000, label: `${label} chat room opened` });

  return { roomId: room.room_id };
}

// Helper: create a fresh listing and open first paid chat. Returns { listingId, roomId }
async function openFirstPaidChat(api, label = '') {
  const cr = await api.createListing(CLIENT_WALLET);
  assertStatus(cr, 201, `${label} createListing`);
  const listingId = cr.body.listing_id;

  await pollUntil(async () => {
    const r = await api.getListing(listingId);
    return r.body.status === 'active' ? true : null;
  }, { timeout: 30000, label: `${label} listing active` });

  const { roomId } = await openPaidChatForListing(api, listingId, PEER_WALLET, label + ' chat1');
  return { listingId, roomId };
}

export async function run() {
  console.log('\n=== 039: Listing Lifecycle After Paid Chat (New Entitlement Model) ===');
  const t = new Runner('039_listing_completed');

  // ── T1 + T2: two paid chats close → listing permanently closed ──────────────────
  {
    const srv = new TestServer({ devMode: true });
    try {
      await srv.start();
      const api = new ApiClient(srv.base);

      await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      await api.verifyWallet(PEER_WALLET,   'BTC', 'peer');
      await api.verifyWallet(PEER2_WALLET,  'BTC', 'peer');
      await api.verifyWallet(PEER3_WALLET,  'BTC', 'peer');

      let listingId, roomId1, roomId2;

      await t.run('setup T1: open first paid chat room', async () => {
        ({ listingId, roomId: roomId1 } = await openFirstPaidChat(api, 'T1-chat1'));
      });

      await t.run('T1a: first chat closes → listing reopens (count=1 < 2)', async () => {
        // Peer closes first
        const pc = await api.closeChat(roomId1, PEER_WALLET);
        assertStatus(pc, 200, 'T1a peer close');
        // Client closes (final close)
        const cc = await api.closeChat(roomId1, CLIENT_WALLET);
        assertStatus(cc, 200, 'T1a client close');

        // Listing should be 'active' now (count=1 < 2 → reopened for second peer)
        const listing = await api.getListing(listingId);
        if (listing.body.status !== 'active') {
          throw new Error(`T1a: Expected listing status=active after first chat closed, got ${listing.body.status}`);
        }
        // Listing must appear on board (second peer can respond)
        const board = await api.getBoard('new_york');
        if (!Array.isArray(board.body) || !board.body.find(l => l.id === listingId)) {
          throw new Error('T1a: Listing not on board after first chat closed — should reopen for second peer');
        }
        // opened_chats_count must be 1
        const count = parseInt(srv.db(`SELECT opened_chats_count FROM listings WHERE id='${listingId}'`), 10);
        if (count !== 1) throw new Error(`T1a: Expected opened_chats_count=1, got ${count}`);
      });

      await t.run('T1b: open second paid chat room', async () => {
        ({ roomId: roomId2 } = await openPaidChatForListing(api, listingId, PEER2_WALLET, 'T1-chat2'));
      });

      await t.run('T1c: listing status=matched while second chat active (count=2)', async () => {
        const listing = await api.getListing(listingId);
        if (listing.body.status !== 'closed') {
          throw new Error(`T1c: Expected listing closed after second chat opened, got ${listing.body.status}`);
        }
        const count = parseInt(srv.db(`SELECT opened_chats_count FROM listings WHERE id='${listingId}'`), 10);
        if (count !== 2) throw new Error(`T1c: Expected opened_chats_count=2, got ${count}`);
      });

      await t.run('T1d: second chat closes → listing permanently closed (LI-1)', async () => {
        const pc = await api.closeChat(roomId2, PEER2_WALLET);
        assertStatus(pc, 200, 'T1d peer2 close');
        const cc = await api.closeChat(roomId2, CLIENT_WALLET);
        assertStatus(cc, 200, 'T1d client close');

        const listing = await api.getListing(listingId);
        if (listing.body.status !== 'closed') {
          throw new Error(`T1d: Expected listing status=closed after second chat closed, got ${listing.body.status}`);
        }

        // Listing must NOT appear on board
        const board = await api.getBoard('new_york');
        if (Array.isArray(board.body) && board.body.find(l => l.id === listingId)) {
          throw new Error('T1d: Listing returned to board after 2 paid chats — LI-1 violated');
        }
      });

      await t.run('T2: peer cannot respond to a closed listing (404)', async () => {
        const peerKeys3 = newKeypair();
        const rr = await api.respond(listingId, PEER3_WALLET, peerKeys3.pub);
        if (rr.status !== 404) {
          throw new Error(`T2: Expected 404 responding to closed listing, got ${rr.status}`);
        }
      });

    } finally {
      await srv.stop();
    }
  }

  // ── T3: accepted response + chat invoice expires → listing restored to active ──
  {
    const srv = new TestServer({ devMode: true });
    try {
      await srv.start();
      const api = new ApiClient(srv.base);

      await t.run('T3: invoice expires without payment → listing returns to active, slot freed', async () => {
        const tokenClient = srv.registerDirect(CLIENT_WALLET, 'client');
        const tokenPeer   = srv.registerDirect(PEER_WALLET,   'peer');
        api.tokens[CLIENT_WALLET] = { token: tokenClient, role: 'client' };
        api.tokens[PEER_WALLET]   = { token: tokenPeer,   role: 'peer' };

        const now = Math.floor(Date.now() / 1000);

        const clientHash = srv.db(
          `SELECT wallet_hash FROM sessions WHERE role='client' ORDER BY created_at DESC LIMIT 1`
        );
        const peerHash = srv.db(
          `SELECT wallet_hash FROM sessions WHERE role='peer' ORDER BY created_at DESC LIMIT 1`
        );

        const listingId  = `lst_039_t3_${now}`;
        const responseId = `rsp_039_t3_${now}`;
        const invoiceId  = `inv_039_t3_${now}`;

        // Inject: listing (matched, count=0) ← response (accepted) ← invoice (expired, no chat room)
        srv.db(
          `INSERT INTO listings (id, city, dependency_type, help_type, urgency, languages, wallet_hash, visible_until, created_at, status, is_sample, opened_chats_count) ` +
          `VALUES ('${listingId}', 'new_york', 'alcohol', 'crisis', 'urgent', '["en"]', '${clientHash}', ${now + 3600}, ${now}, 'matched', 0, 0)`
        );
        srv.db(
          `INSERT INTO responses (id, listing_id, counselor_hash, counselor_pubkey, status, created_at) ` +
          `VALUES ('${responseId}', '${listingId}', '${peerHash}', 'dummy-pubkey', 'accepted', ${now})`
        );
        srv.db(
          `INSERT INTO invoices (id, type, address, amount_usd, currency, response_id, status, created_at) ` +
          `VALUES ('${invoiceId}', 'chat', 'bc1test', 25.0, 'BTC', '${responseId}', 'expired', ${now})`
        );

        // TTL cleaner runs every 5s — wait for step 2d to fire and restore listing
        await pollUntil(async () => {
          const r = await api.getListing(listingId);
          return r.body.status === 'active' ? true : null;
        }, { timeout: 30000, label: 'T3 listing restored to active' });

        // Response must be closed (peer slot freed)
        const respStatus = srv.db(`SELECT status FROM responses WHERE id = '${responseId}'`);
        if (respStatus !== 'closed') {
          throw new Error(`T3: Expected response status=closed after invoice expired, got ${respStatus}`);
        }

        // Listing must appear on board (a new peer can now respond)
        const board = await api.getBoard('new_york');
        if (!Array.isArray(board.body) || !board.body.find(l => l.id === listingId)) {
          throw new Error('T3: Listing not on board after unpaid invoice expired — client is stuck');
        }
      });

    } finally {
      await srv.stop();
    }
  }

  // ── T4: first paid chat closes → listing reopens; second paid chat closes → listing closed ──────
  {
    const srv = new TestServer({ devMode: true });
    try {
      await srv.start();
      const api = new ApiClient(srv.base);

      await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      await api.verifyWallet(PEER_WALLET,   'BTC', 'peer');
      await api.verifyWallet(PEER2_WALLET,  'BTC', 'peer');

      let listingId, roomId1, roomId2;

      await t.run('setup T4: open first paid chat', async () => {
        ({ listingId, roomId: roomId1 } = await openFirstPaidChat(api, 'T4'));
      });

      await t.run('T4a: immediate close of first paid chat → listing reopens (count=1 < 2)', async () => {
        // devMode allows 0-duration (minDuration=0)
        const pc = await api.closeChat(roomId1, PEER_WALLET);
        assertStatus(pc, 200, 'T4a peer close');
        const cc = await api.closeChat(roomId1, CLIENT_WALLET);
        assertStatus(cc, 200, 'T4a client close');

        const listing = await api.getListing(listingId);
        if (listing.body.status !== 'active') {
          throw new Error(`T4a: Expected listing active after first chat closed, got ${listing.body.status}`);
        }
      });

      await t.run('T4b: open second paid chat', async () => {
        ({ roomId: roomId2 } = await openPaidChatForListing(api, listingId, PEER2_WALLET, 'T4-chat2'));
      });

      await t.run('T4c: immediate close of second paid chat → listing closed (count=2)', async () => {
        const pc = await api.closeChat(roomId2, PEER2_WALLET);
        assertStatus(pc, 200, 'T4c peer2 close');
        const cc = await api.closeChat(roomId2, CLIENT_WALLET);
        assertStatus(cc, 200, 'T4c client close');

        const listing = await api.getListing(listingId);
        if (listing.body.status !== 'closed') {
          throw new Error(`T4c: Expected listing closed after both paid chats ended, got ${listing.body.status}`);
        }

        const board = await api.getBoard('new_york');
        if (Array.isArray(board.body) && board.body.find(l => l.id === listingId)) {
          throw new Error('T4c: Listing returned to board after 2 paid chats — LI-1 violated');
        }
      });

    } finally {
      await srv.stop();
    }
  }

  // ── T5: TTL expires half-closed room at count=1 → listing reopens ────────────
  {
    const srv = new TestServer({ devMode: true });
    try {
      await srv.start();
      const api = new ApiClient(srv.base);

      await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      await api.verifyWallet(PEER_WALLET,   'BTC', 'peer');

      let listingId, roomId;

      await t.run('setup T5: open first paid chat', async () => {
        ({ listingId, roomId } = await openFirstPaidChat(api, 'T5'));
      });

      await t.run('T5: peer closes → TTL expires half-closed room (count=1) → listing reopens', async () => {
        // Peer leaves — room becomes peer_left (half-closed)
        const pc = await api.closeChat(roomId, PEER_WALLET);
        assertStatus(pc, 200, 'T5 peer close');

        // Fast-forward: set peer_left_at far in the past so TTL cleaner fires
        srv.db(`UPDATE chat_rooms SET peer_left_at = 1 WHERE id = '${roomId}'`);

        // TTL cleaner runs every 5s — wait for expireHalfClosedRooms to fire
        await pollUntil(async () => {
          const status = srv.db(`SELECT status FROM chat_rooms WHERE id = '${roomId}'`);
          return status === 'expired' ? true : null;
        }, { timeout: 30000, label: 'T5 room expired by TTL' });

        // Listing should be 'active' (count=1 < 2 → reopened)
        const listing = await api.getListing(listingId);
        if (listing.body.status !== 'active') {
          throw new Error(`T5: Expected listing active after TTL expiry of half-closed room (count=1), got ${listing.body.status}`);
        }

        // Listing SHOULD be on board (second peer can respond)
        const board = await api.getBoard('new_york');
        if (!Array.isArray(board.body) || !board.body.find(l => l.id === listingId)) {
          throw new Error('T5: Listing not on board after TTL-expired half-closed room with count=1 — should reopen');
        }
      });

    } finally {
      await srv.stop();
    }
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
