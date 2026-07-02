// 031_concurrent_accept
// Свойство: клиент отправляет два accept'а на два разных отклика ОДНОВРЕМЕННО
// → ровно один accept проходит, создаётся ровно одна запись с status='accepted'.
// Ловит TOCTOU: check-then-act без атомарной гарантии.
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_A_WALLET = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';
const PEER_B_WALLET = '1CounterpartyXXXXXXXXXXXXXXXUWLpVr';

export async function run() {
  console.log('\n=== 031: Concurrent Accept (TOCTOU) ===');
  const srv = new TestServer();
  const t = new Runner('031_concurrent_accept');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    let listingId, respAId, respBId;

    await t.run('register client and two peers', async () => {
      await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      await api.verifyWallet(PEER_A_WALLET, 'BTC', 'peer');
      await api.verifyWallet(PEER_B_WALLET, 'BTC', 'peer');
    });

    await t.run('client creates listing', async () => {
      const r = await api.createListing(CLIENT_WALLET);
      assertStatus(r, 201, 'create listing');
      listingId = r.body.listing_id;
    });

    await t.run('listing becomes active', async () => {
      await pollUntil(async () => {
        const r = await api.getListing(listingId);
        return r.body.status === 'active' ? true : null;
      }, { timeout: 30000, label: 'listing active' });
    });

    await t.run('both peers respond', async () => {
      const rA = await api.respond(listingId, PEER_A_WALLET, newKeypair().pub);
      assertStatus(rA, 201, 'peer A respond');
      respAId = rA.body.response_id;

      const rB = await api.respond(listingId, PEER_B_WALLET, newKeypair().pub);
      assertStatus(rB, 201, 'peer B respond');
      respBId = rB.body.response_id;
    });

    await t.run('client accepts both simultaneously — only one succeeds', async () => {
      const clientPub = newKeypair().pub;

      const [rA, rB] = await Promise.allSettled([
        api.post(`/response/${respAId}/accept`, { client_pubkey: clientPub, currency: 'BTC' }, CLIENT_WALLET),
        api.post(`/response/${respBId}/accept`, { client_pubkey: clientPub, currency: 'BTC' }, CLIENT_WALLET),
      ]);

      const statuses = [rA, rB].map(r =>
        r.status === 'fulfilled' ? r.value.status : 'network-error'
      );
      const okCount = statuses.filter(s => s === 200).length;

      if (okCount !== 1) {
        throw new Error(
          `Expected exactly 1 successful accept, got ${okCount} (statuses: ${statuses.join(', ')})`
        );
      }

      // Loser must not be 5xx
      const loserCode = statuses.find(s => s !== 200 && s !== 'network-error');
      if (loserCode && loserCode >= 500) {
        throw new Error(`Losing accept returned ${loserCode} instead of 4xx`);
      }
    });

    await t.run('DB: exactly one accepted response for listing', async () => {
      const count = parseInt(srv.db(
        `SELECT COUNT(*) FROM responses WHERE listing_id='${listingId}' AND status='accepted'`
      ), 10);
      if (count !== 1) {
        throw new Error(`Expected 1 accepted response, found ${count}`);
      }
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
