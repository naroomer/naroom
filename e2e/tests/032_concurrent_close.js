// 032_concurrent_close
// Свойство: обе стороны закрывают чат ОДНОВРЕМЕННО → ровно один переход
// в status='closed', без дублирования побочных эффектов.
// Идемпотентность close: второй запрос возвращает 200/410, но не 500.
import { TestServer, sleep } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_WALLET   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

export async function run() {
  console.log('\n=== 032: Concurrent Close ===');
  const srv = new TestServer();
  const t = new Runner('032_concurrent_close');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);
    let listingId, responseId, roomId;

    await t.run('register client and peer', async () => {
      await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      await api.verifyWallet(PEER_WALLET,   'BTC', 'peer');
    });

    await t.run('client creates listing and it becomes active', async () => {
      const r = await api.createListing(CLIENT_WALLET);
      assertStatus(r, 201, 'create listing');
      listingId = r.body.listing_id;
      await pollUntil(async () => {
        const s = await api.getListing(listingId);
        return s.body.status === 'active' ? true : null;
      }, { timeout: 30000, label: 'listing active' });
    });

    await t.run('peer responds', async () => {
      const r = await api.respond(listingId, PEER_WALLET, newKeypair().pub);
      assertStatus(r, 201, 'respond');
      responseId = r.body.response_id;
    });

    await t.run('client accepts peer', async () => {
      const r = await api.post(
        `/response/${responseId}/accept`,
        { client_pubkey: newKeypair().pub, currency: 'BTC' },
        CLIENT_WALLET
      );
      assertStatus(r, 200, 'accept');
    });

    await t.run('chat room opens after invoice confirmed', async () => {
      await pollUntil(async () => {
        const r = await api.getListingChatRoom(listingId, CLIENT_WALLET);
        if (r.status === 200 && r.body.room_id) {
          roomId = r.body.room_id;
          return true;
        }
        return null;
      }, { timeout: 30000, label: 'chat room open' });
    });

    await t.run('both sides close simultaneously — no 5xx, room closed exactly once', async () => {
      const [rc, rp] = await Promise.allSettled([
        api.post(`/chat/${roomId}/close`, {}, CLIENT_WALLET),
        api.post(`/chat/${roomId}/close`, {}, PEER_WALLET),
      ]);

      const codes = [rc, rp].map(r => r.status === 'fulfilled' ? r.value.status : 'network-error');

      // Neither response may be 5xx
      for (const c of codes) {
        if (typeof c === 'number' && c >= 500) {
          throw new Error(`Concurrent close returned ${c} — must not be 5xx (codes: ${codes.join(', ')})`);
        }
      }

      // Small wait for DB writes to settle
      await sleep(300);

      // Exactly one closed row in DB
      const count = parseInt(srv.db(
        `SELECT COUNT(*) FROM chat_rooms WHERE id='${roomId}' AND status='closed'`
      ), 10);
      if (count !== 1) {
        throw new Error(`Expected 1 closed room, got ${count}`);
      }
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
