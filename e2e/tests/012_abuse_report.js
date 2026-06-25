// 012_abuse_report.js — abuse report requires prior room participation
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_WALLET   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

export async function run() {
  console.log('\n=== 012: Abuse Report Guard ===');
  const srv = new TestServer();
  const t = new Runner('012_abuse_report');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);
    const clientKeys = newKeypair();
    const peerKeys   = newKeypair();

    await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
    await api.verifyWallet(PEER_WALLET, 'BTC', 'peer');

    await t.run('abuse report without session → 401', async () => {
      const r = await fetch(`${srv.base}/abuse-report`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ room_id: 'fake_room', categories: ['misuse'] }),
      });
      if (r.status !== 401) throw new Error(`Expected 401, got ${r.status}`);
    });

    await t.run('peer reports client for non-existent room → 403', async () => {
      const r = await api.abuseReport('nonexistent_room_id', ['misuse'], PEER_WALLET);
      assertStatus(r, 403, 'fake room abuse report');
    });

    await t.run('client cannot submit abuse report (only peer role)', async () => {
      const r = await api.abuseReport('any_room_id', ['misuse'], CLIENT_WALLET);
      if (r.status !== 403) throw new Error(`Expected 403 for client role, got ${r.status}: ${JSON.stringify(r.body)}`);
    });

    // Set up a real room so peer can legitimately report
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

    await t.run('peer reports client for real room → 200', async () => {
      const r = await api.abuseReport(roomId, ['misuse'], PEER_WALLET);
      assertStatus(r, 200, 'valid abuse report');
    });

    await t.run('peer cannot report same client twice (dedup) → 409', async () => {
      const r = await api.abuseReport(roomId, ['other'], PEER_WALLET);
      assertStatus(r, 409, 'duplicate abuse report');
    });

    await t.run('invalid category → 400', async () => {
      const r = await api.abuseReport(roomId, ['hacking'], PEER_WALLET);
      if (r.status !== 400) throw new Error(`Expected 400 for invalid category, got ${r.status}`);
    });

    await t.run('abuse counters updated in DB', async () => {
      const count = srv.db(`SELECT total FROM abuse_counters LIMIT 1`);
      if (parseInt(count, 10) < 1) throw new Error(`abuse total=${count}, expected >=1`);
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
