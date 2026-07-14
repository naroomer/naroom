// 002_stale_room_guard.js — stale chat_room from previous session must not be returned
// Simulates: peer already has an old active room, new listing starts fresh
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus, assertNoRoom, pollUntil, sleep } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_WALLET   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

export async function run() {
  console.log('\n=== 002: Stale Room Guard ===');
  const srv = new TestServer();
  const t = new Runner('002_stale_room_guard');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);
    const clientKeys = newKeypair();
    const peerKeys   = newKeypair();

    // Verify wallets first so wallet_hash exists in DB before we inject the stale room
    await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
    await api.verifyWallet(PEER_WALLET, 'BTC', 'peer');

    // ── Create a "old" room manually in DB ────────────────────────────────
    // Simulate stale room left from a previous session
    await t.run('inject stale active room for peer address', async () => {
      // Use a DIFFERENT client hash so the stale room doesn't block acceptResponse
      // The point is: same peer principal_id → peer's poll would pick it up if not scoped
      const staleClientHash    = 'hash_stale_client_0000000000000000000000000000000000000000000000';
      // Get peer's wallet_hash and principal_id (set by /session/init + /wallet/register)
      const staleCounselorHash = srv.db(`SELECT wallet_hash FROM wallet_sessions WHERE role='peer' LIMIT 1`);
      if (!staleCounselorHash) throw new Error('peer wallet_hash not found — verifyWallet must run first');
      const peerPrincipalId = srv.db(`SELECT principal_id FROM sessions WHERE wallet_hash='${staleCounselorHash}' AND revoked_at IS NULL AND principal_id IS NOT NULL LIMIT 1`);
      if (!peerPrincipalId) throw new Error('peer principal_id not found — verifyWallet must run first');
      srv.db(`
        INSERT INTO chat_rooms (id, listing_id, response_id, client_hash, counselor_hash,
          client_pubkey, counselor_pubkey, started_at, expires_at, status, counselor_principal_id)
        VALUES (
          'room_stale_test', 'lst_old', 'rsp_old',
          '${staleClientHash}', '${staleCounselorHash}',
          '${clientKeys.pub}', '${peerKeys.pub}',
          ${Math.floor(Date.now()/1000) - 3600},
          ${Math.floor(Date.now()/1000) + 3600},
          'active', '${peerPrincipalId}'
        )
      `);
    });

    // ── Start new flow with new listing ──────────────────────────────────
    const createR = await api.createListing(CLIENT_WALLET);
    const listingId = createR.body.listing_id;

    // Wait for listing to activate
    await pollUntil(async () => {
      const r = await api.getListing(listingId);
      return r.body.status === 'active' ? true : null;
    }, { timeout: 45000, label: 'listing active' });

    await t.run('peer poll with OLD listing_id returns stale room (shows why scoping matters)', async () => {
      // Without listing_id scoping, peer would see the stale room
      const r = await api.getPeerChatroom(PEER_WALLET, 'lst_old');
      // This SHOULD return the stale room (it exists for lst_old)
      if (r.status !== 200 || !r.body.room_id) throw new Error('Stale room not found for lst_old (expected to find it)');
    });

    await t.run('peer poll with NEW listing_id does NOT return stale room', async () => {
      // This is the fixed behavior — scoped to current listing
      await assertNoRoom(api, PEER_WALLET, listingId, 'new listing before accept');
    });

    // Continue with proper flow
    await api.respond(listingId, PEER_WALLET, peerKeys.pub);

    await t.run('peer still cannot see chat room before accept (even with stale in DB)', async () => {
      await assertNoRoom(api, PEER_WALLET, listingId, 'after respond, before accept');
    });

    const respR = await api.getResponses(listingId, CLIENT_WALLET);
    const responseId = respR.body[0].id;

    await api.acceptResponse(responseId, CLIENT_WALLET, clientKeys.pub);

    await t.run('peer sees only the NEW room after accept+payment', async () => {
      const room = await pollUntil(async () => {
        const r = await api.getPeerChatroom(PEER_WALLET, listingId);
        return r.status === 200 ? r.body : null;
      }, { timeout: 45000, label: 'new room for peer' });

      if (room.room_id === 'room_stale_test') throw new Error('Got stale room instead of new one!');
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
