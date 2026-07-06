// 006_state_bleed.js — verify state isolation between flows in same DB
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus, assertNoRoom, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_WALLET   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

export async function run() {
  console.log('\n=== 006: State Bleed Detection ===');
  const srv = new TestServer();
  const t = new Runner('006_state_bleed');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);
    const clientKeys = newKeypair();
    const peerKeys   = newKeypair();

    // ── Flow 1: complete full chat ────────────────────────────────────────
    await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
    await api.verifyWallet(PEER_WALLET, 'BTC', 'peer');

    const cr1 = await api.createListing(CLIENT_WALLET);
    const listingId1 = cr1.body.listing_id;
    const testStart = Math.floor(Date.now() / 1000);

    await pollUntil(async () => {
      const r = await api.getListing(listingId1);
      return r.body.status === 'active' ? true : null;
    }, { timeout: 45000, label: 'listing1 active' });

    await api.respond(listingId1, PEER_WALLET, peerKeys.pub);
    const rr1 = await api.getResponses(listingId1, CLIENT_WALLET);
    const responseId1 = rr1.body[0].id;
    await api.acceptResponse(responseId1, CLIENT_WALLET, clientKeys.pub);

    const room1 = await pollUntil(async () => {
      const r = await api.getPeerChatroom(PEER_WALLET, listingId1);
      return r.status === 200 ? r.body : null;
    }, { timeout: 45000, label: 'room1' });
    const roomId1 = room1.room_id;

    await t.run('DB: room started_at >= test start', async () => {
      const val = parseInt(srv.db(`SELECT started_at FROM chat_rooms WHERE id='${roomId1}'`), 10);
      if (val < testStart) throw new Error(`room started_at=${val} < testStart=${testStart}`);
    });

    await t.run('DB: room.response_id = current responseId', async () => {
      const val = srv.db(`SELECT response_id FROM chat_rooms WHERE id='${roomId1}'`);
      if (val !== responseId1) throw new Error(`response_id mismatch: ${val} != ${responseId1}`);
    });

    await t.run('DB: room.listing_id = current listingId', async () => {
      const val = srv.db(`SELECT listing_id FROM chat_rooms WHERE id='${roomId1}'`);
      if (val !== listingId1) throw new Error(`listing_id mismatch: ${val} != ${listingId1}`);
    });

    // Close flow 1 (symmetric: both sides must close)
    // Peer closes first, then client completes the full close → listing restored
    await api.closeChat(roomId1, PEER_WALLET);
    await api.closeChat(roomId1, CLIENT_WALLET);

    // ── Flow 2: listing permanently closed after paid chat (LI-1) ─────────
    await t.run('listing permanently closed after paid chat (LI-1)', async () => {
      const r = await api.getListing(listingId1);
      if (r.body.status !== 'closed') throw new Error(`Listing status=${r.body.status}, expected closed`);
    });

    await t.run('closed listing does not appear on board', async () => {
      const r = await api.getBoard('new_york');
      const found = r.body.find(l => l.id === listingId1);
      if (found) throw new Error('Closed listing returned to board — LI-1 violated');
    });

    await t.run('client can create new listing after paid session completes', async () => {
      await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      const r = await api.createListing(CLIENT_WALLET, 'london');
      assertStatus(r, 201, 'new listing after paid session');
    });

    await t.run('peer poll for closed room returns no active room', async () => {
      const r = await api.getPeerChatroom(PEER_WALLET, listingId1);
      if (r.status === 200 && r.body.room_id === roomId1) {
        throw new Error('Old closed room returned');
      }
    });

    await t.run('peer cannot respond to permanently closed listing (404)', async () => {
      await api.verifyWallet(PEER_WALLET, 'BTC', 'peer');
      const r = await api.respond(listingId1, PEER_WALLET, peerKeys.pub);
      if (r.status !== 404) throw new Error(`Expected 404 responding to closed listing, got ${r.status}`);
    });

    await t.run('DB: only one active chat_room total (no state bleed)', async () => {
      const count = parseInt(srv.db(`SELECT COUNT(*) FROM chat_rooms WHERE status='active'`), 10);
      if (count !== 0) throw new Error(`Expected 0 active rooms after close, got ${count}`);
    });

    await t.run('listing status=closed and not on board (LI-1)', async () => {
      const status = srv.db(`SELECT status FROM listings WHERE id='${listingId1}'`);
      if (status !== 'closed') throw new Error(`Listing status=${status}, expected closed (LI-1)`);
      const r = await api.getBoard('new_york');
      const found = r.body.find(l => l.id === listingId1);
      if (found) throw new Error('Closed listing on board — LI-1 violated');
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
