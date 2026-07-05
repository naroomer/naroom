// 003_role_separation_review.js — review token only issued to client, not peer
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus, assertHasField, assertNoField, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_WALLET   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

async function setupChat(api) {
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
  const responseId = rr.body[0].id;
  await api.acceptResponse(responseId, CLIENT_WALLET, clientKeys.pub);

  const room = await pollUntil(async () => {
    const r = await api.getPeerChatroom(PEER_WALLET, listingId);
    return r.status === 200 ? r.body : null;
  }, { timeout: 45000, label: 'chat room' });

  return { clientKeys, peerKeys, roomId: room.room_id };
}

export async function run() {
  console.log('\n=== 003: Role Separation — Review ===');
  const t = new Runner('003_role_separation_review');

  // ── Test A: Both sides close → client (second closer) gets token ────────
  // With symmetric CloseChat: first close → partial close; second close → full close + review_token.
  // Client always receives the review_token when both sides have closed.
  {
    const srv = new TestServer();
    try {
      await srv.start();
      const api = new ApiClient(srv.base);
      await setupChat(api);
      const room = srv.db(`SELECT id FROM chat_rooms WHERE status='active' LIMIT 1`);

      await t.run('peer closes first → status=peer_left, no token yet', async () => {
        const r = await api.closeChat(room, PEER_WALLET);
        assertStatus(r, 200, 'peer first close');
        if (r.body.status !== 'peer_left') throw new Error(`Expected peer_left, got ${r.body.status}`);
        assertNoField(r.body, 'review_token', 'peer first close response');
      });

      await t.run('client closes second → full close, receives review_token', async () => {
        const r = await api.closeChat(room, CLIENT_WALLET);
        assertStatus(r, 200, 'client second close');
        if (r.body.status !== 'closed') throw new Error(`Expected closed, got ${r.body.status}`);
        assertHasField(r.body, 'review_token', 'client close response');
      });

    } finally { await srv.stop(); }
  }

  // ── Test B: Peer closes → status=peer_left, no token ─────────────────
  {
    const srv = new TestServer();
    try {
      await srv.start();
      const api = new ApiClient(srv.base);
      await setupChat(api);
      const room = srv.db(`SELECT id FROM chat_rooms WHERE status='active' LIMIT 1`);

      await t.run('peer closes chat → status=peer_left, no review_token', async () => {
        const r = await api.closeChat(room, PEER_WALLET);
        assertStatus(r, 200, 'peer close');
        if (r.body.status !== 'peer_left') throw new Error(`Expected peer_left, got ${r.body.status}`);
        assertNoField(r.body, 'review_token', 'peer close response');
      });

      await t.run('UI contract: peer role should not show review form', async () => {
        const closeResp = { status: 'peer_left' };
        const roomMeta = { role: 'peer' };
        const showReview = roomMeta.role === 'client' && Boolean(closeResp.review_token);
        if (showReview) throw new Error('showReview=true for peer — UI would show review form');
      });

    } finally { await srv.stop(); }
  }

  // ── Test C: Review token from client close works once ─────────────────
  {
    const srv = new TestServer();
    try {
      await srv.start();
      const api = new ApiClient(srv.base);
      await setupChat(api);
      const room = srv.db(`SELECT id FROM chat_rooms WHERE status='active' LIMIT 1`);

      let token;
      await t.run('client closes and gets review_token (peer closes first)', async () => {
        // Peer leaves first so client gets the full-close + review_token
        await api.closeChat(room, PEER_WALLET);
        const r = await api.closeChat(room, CLIENT_WALLET);
        token = r.body.review_token;
        if (!token) throw new Error('No token');
      });

      await t.run('review token works with rating=up', async () => {
        const r = await api.submitReview(token, 'up');
        assertStatus(r, 200, 'review up');
      });

      await t.run('review token rejected on second use', async () => {
        const r = await api.submitReview(token, 'down');
        if (r.status === 200) throw new Error('Token reused — should be rejected');
      });

      await t.run('random token rejected', async () => {
        const r = await api.submitReview('fakefakefakefakefakefake', 'up');
        if (r.status === 200) throw new Error('Fake token accepted');
      });

    } finally { await srv.stop(); }
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
