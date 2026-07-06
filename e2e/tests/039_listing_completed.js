// 039_listing_completed.js — listing lifecycle after paid chat
//
// Product invariant (LI-1): once a paid chat room has been created for a listing,
// that listing must NEVER return to the public board regardless of how the chat ends.
// Only if the peer accepted but never paid (chat invoice expired/rejected) may the
// listing return to 'active' so a new peer can respond.
//
// Bugs fixed:
//   chat_ws.go CloseChat: SET status='active' → 'closed'
//   ttl_cleaner.go expireHalfClosedRooms: SET status='active' → 'closed'
//   ttl_cleaner.go step 2d: close accepted responses on expired chat invoice, restore listing
//
// Tests:
//   T1: paid chat created → both sides close → listing NOT on board, status='closed'
//   T2: paid chat created → both sides close → peer cannot respond again (404)
//   T3: accepted response + chat invoice expires (no payment) → listing returns to 'active', peer slot freed
//   T4: very short paid chat → listing still closed (duration does not matter)
//   T5: paid chat created → TTL expires half-closed room → listing still 'closed'

import { TestServer, sleep } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_WALLET   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';
const PEER2_WALLET  = '1CounterpartyXXXXXXXXXXXXXXXUWLpVr';

// Full flow helper: returns { listingId, roomId }
async function openPaidChat(api, t, label = '') {
  const clientKeys = newKeypair();
  const peerKeys   = newKeypair();

  const cr = await api.createListing(CLIENT_WALLET);
  assertStatus(cr, 201, `${label} createListing`);
  const listingId = cr.body.listing_id;

  await pollUntil(async () => {
    const r = await api.getListing(listingId);
    return r.body.status === 'active' ? true : null;
  }, { timeout: 30000, label: `${label} listing active` });

  const pr = await api.respond(listingId, PEER_WALLET, peerKeys.pub);
  assertStatus(pr, 201, `${label} respond`);
  const responseId = pr.body.response_id;

  const ar = await api.acceptResponse(responseId, CLIENT_WALLET, clientKeys.pub);
  assertStatus(ar, 200, `${label} acceptResponse`);

  const room = await pollUntil(async () => {
    const r = await api.get('/peer/chatroom?listing_id=' + encodeURIComponent(listingId), PEER_WALLET);
    return r.status === 200 ? r.body : null;
  }, { timeout: 30000, label: `${label} chat room opened` });

  return { listingId, roomId: room.room_id };
}

export async function run() {
  console.log('\n=== 039: Listing Lifecycle After Paid Chat ===');
  const t = new Runner('039_listing_completed');

  // ── T1 + T2: both sides close → listing permanently closed ──────────────────
  {
    const srv = new TestServer({ devMode: true });
    try {
      await srv.start();
      const api = new ApiClient(srv.base);

      await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      await api.verifyWallet(PEER_WALLET,   'BTC', 'peer');
      await api.verifyWallet(PEER2_WALLET,  'BTC', 'peer');

      let listingId, roomId;

      await t.run('setup: open paid chat room', async () => {
        ({ listingId, roomId } = await openPaidChat(api, t, 'T1'));
      });

      await t.run('T1: both sides close → listing NOT on board and status=closed', async () => {
        // Peer closes first
        const pc = await api.closeChat(roomId, PEER_WALLET);
        assertStatus(pc, 200, 'peer close');

        // Client closes (final close)
        const cc = await api.closeChat(roomId, CLIENT_WALLET);
        assertStatus(cc, 200, 'client close');

        // Listing must be 'closed' now
        const listing = await api.getListing(listingId);
        if (listing.body.status !== 'closed') {
          throw new Error(`Expected listing status=closed after paid chat, got ${listing.body.status}`);
        }

        // Listing must NOT appear on the board (board.body is a raw array)
        const board = await api.getBoard('new_york');
        if (Array.isArray(board.body) && board.body.find(l => l.id === listingId)) {
          throw new Error('Listing returned to board after paid chat was closed — LI-1 violated');
        }
      });

      await t.run('T2: peer cannot respond to a closed listing (404)', async () => {
        const peerKeys2 = newKeypair();
        const rr = await api.respond(listingId, PEER2_WALLET, peerKeys2.pub);
        if (rr.status !== 404) {
          throw new Error(`Expected 404 responding to closed listing, got ${rr.status}`);
        }
      });

    } finally {
      await srv.stop();
    }
  }

  // ── T3: accepted response + chat invoice expires → listing restored to active ──
  // devMode auto-confirms invoices, so we inject DB state directly rather than
  // racing the invoice watcher.
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

        // Retrieve wallet hashes from sessions table (registerDirect just created them)
        const clientHash = srv.db(
          `SELECT wallet_hash FROM sessions WHERE role='client' ORDER BY created_at DESC LIMIT 1`
        );
        const peerHash = srv.db(
          `SELECT wallet_hash FROM sessions WHERE role='peer' ORDER BY created_at DESC LIMIT 1`
        );

        const listingId  = `lst_039_t3_${now}`;
        const responseId = `rsp_039_t3_${now}`;
        const invoiceId  = `inv_039_t3_${now}`;

        // Inject: listing (matched) ← response (accepted) ← invoice (expired, no chat room)
        srv.db(
          `INSERT INTO listings (id, city, dependency_type, help_type, urgency, languages, wallet_hash, visible_until, created_at, status, is_sample) ` +
          `VALUES ('${listingId}', 'new_york', 'alcohol', 'crisis', 'urgent', '["en"]', '${clientHash}', ${now + 3600}, ${now}, 'matched', 0)`
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
          throw new Error(`Expected response status=closed after invoice expired, got ${respStatus}`);
        }

        // Listing must appear on board (a new peer can now respond)
        const board = await api.getBoard('new_york');
        if (!Array.isArray(board.body) || !board.body.find(l => l.id === listingId)) {
          throw new Error('Listing not on board after unpaid invoice expired — client is stuck');
        }
      });

    } finally {
      await srv.stop();
    }
  }

  // ── T4: very short paid chat → listing still closed ──────────────────────────
  {
    const srv = new TestServer({ devMode: true });
    try {
      await srv.start();
      const api = new ApiClient(srv.base);

      await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      await api.verifyWallet(PEER_WALLET,   'BTC', 'peer');

      let listingId, roomId;

      await t.run('setup T4: open paid chat', async () => {
        ({ listingId, roomId } = await openPaidChat(api, t, 'T4'));
      });

      await t.run('T4: immediate close (0-duration paid chat) → listing still closed', async () => {
        // Close immediately — devMode allows 0-duration (minDuration=0)
        const pc = await api.closeChat(roomId, PEER_WALLET);
        assertStatus(pc, 200, 'T4 peer close');
        const cc = await api.closeChat(roomId, CLIENT_WALLET);
        assertStatus(cc, 200, 'T4 client close');

        const listing = await api.getListing(listingId);
        if (listing.body.status !== 'closed') {
          throw new Error(`T4: Expected listing closed even after very short chat, got ${listing.body.status}`);
        }

        const board = await api.getBoard('new_york');
        if (Array.isArray(board.body) && board.body.find(l => l.id === listingId)) {
          throw new Error('T4: Listing returned to board after very short paid chat — LI-1 violated');
        }
      });

    } finally {
      await srv.stop();
    }
  }

  // ── T5: TTL expires half-closed room → listing still 'closed' ────────────────
  {
    const srv = new TestServer({ devMode: true });
    try {
      await srv.start();
      const api = new ApiClient(srv.base);

      await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      await api.verifyWallet(PEER_WALLET,   'BTC', 'peer');

      let listingId, roomId;

      await t.run('setup T5: open paid chat', async () => {
        ({ listingId, roomId } = await openPaidChat(api, t, 'T5'));
      });

      await t.run('T5: peer closes, TTL expires half-closed room → listing stays closed', async () => {
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

        // Listing must NOT be restored to active
        const listing = await api.getListing(listingId);
        if (listing.body.status !== 'closed') {
          throw new Error(`T5: Expected listing closed after TTL expiry of half-closed paid room, got ${listing.body.status}`);
        }

        const board = await api.getBoard('new_york');
        if (Array.isArray(board.body) && board.body.find(l => l.id === listingId)) {
          throw new Error('T5: Listing returned to board after TTL-expired half-closed paid room — LI-1 violated');
        }
      });

    } finally {
      await srv.stop();
    }
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
