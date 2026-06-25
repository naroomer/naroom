// 011_peer_left_expiry.js — peer_left room expires → listing restored (no review token)
import { TestServer, sleep } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_WALLET   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

export async function run() {
  console.log('\n=== 011: peer_left Room Expiry → Listing Restored ===');
  const srv = new TestServer();
  const t = new Runner('011_peer_left_expiry');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);
    const clientKeys = newKeypair();
    const peerKeys   = newKeypair();

    await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
    await api.verifyWallet(PEER_WALLET, 'BTC', 'peer');

    const cr = await api.createListing(CLIENT_WALLET);
    const listingId = cr.body.listing_id;

    await pollUntil(async () => {
      const r = await api.getListing(listingId);
      return r.body.status === 'active' ? true : null;
    }, { timeout: 45000, label: 'listing active' });

    await api.respond(listingId, PEER_WALLET, peerKeys.pub);
    const rr = await api.getResponses(listingId, CLIENT_WALLET);
    await api.acceptResponse(rr.body[0].id, CLIENT_WALLET, clientKeys.pub);

    const room = await pollUntil(async () => {
      const r = await api.getPeerChatroom(PEER_WALLET, listingId);
      return r.status === 200 ? r.body : null;
    }, { timeout: 45000, label: 'chat room' });
    const roomId = room.room_id;

    await t.run('peer closes → room status = peer_left', async () => {
      const r = await api.closeChat(roomId, PEER_WALLET);
      assertStatus(r, 200, 'peer close');
      if (r.body.status !== 'peer_left') throw new Error(`Expected peer_left, got ${r.body.status}`);
    });

    await t.run('room is peer_left in DB', async () => {
      const status = srv.db(`SELECT status FROM chat_rooms WHERE id='${roomId}'`);
      if (status !== 'peer_left') throw new Error(`DB status=${status}, expected peer_left`);
    });

    await t.run('listing is still matched (peer_left room open)', async () => {
      const r = await api.getListing(listingId);
      if (r.body.status !== 'matched') throw new Error(`Listing status=${r.body.status}, expected matched while room peer_left`);
    });

    // Manually expire the room by backdating expires_at
    await t.run('inject: backdate room expires_at to simulate expiry', async () => {
      srv.db(`UPDATE chat_rooms SET expires_at = ${Math.floor(Date.now()/1000) - 10} WHERE id='${roomId}'`);
    });

    // Wait for TTL cleaner to pick it up (runs every TTL_CLEAN_INTERVAL seconds, default ~30s in dev)
    await t.run('TTL cleaner expires peer_left room and restores listing', async () => {
      // TTL_CLEAN_INTERVAL=5s in tests, so we should see it within 15s
      const listing = await pollUntil(async () => {
        const r = await api.getListing(listingId);
        return r.body.status === 'active' ? r.body : null;
      }, { timeout: 30000, interval: 2000, label: 'listing restored after peer_left expiry' });
      if (listing.status !== 'active') throw new Error('Listing not restored');
    });

    await t.run('expired room has status=expired in DB', async () => {
      const status = srv.db(`SELECT status FROM chat_rooms WHERE id='${roomId}'`);
      if (status !== 'expired') throw new Error(`DB room status=${status}, expected expired`);
    });

    await t.run('no review_token issued for peer_left expiry (client did not close)', async () => {
      const count = srv.db(`SELECT COUNT(*) FROM review_tokens`);
      if (parseInt(count, 10) > 0) throw new Error('review_token should NOT be issued for peer_left expiry');
    });

    await t.run('listing appears on board again', async () => {
      const r = await api.getBoard('new_york');
      const found = r.body.find(l => l.id === listingId);
      if (!found) throw new Error('Restored listing not on board after peer_left expiry');
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
