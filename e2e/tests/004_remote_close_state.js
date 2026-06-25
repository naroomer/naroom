// 004_remote_close_state.js — when one side closes, other side detects terminal state
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair, encrypt } from '../lib/crypto.js';
import { ChatWS } from '../lib/ws.js';
import { assertStatus, pollUntil, sleep } from '../lib/assert.js';
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
  console.log('\n=== 004: Remote Close — Other Side Detects Terminal State ===');
  const t = new Runner('004_remote_close_state');

  // ── Test A: Client closes → peer detects closed room ──────────────────
  {
    const srv = new TestServer();
    try {
      await srv.start();
      const api = new ApiClient(srv.base);
      const { clientKeys, peerKeys, roomId } = await setupChat(api);

      let peerWS;

      await t.run('peer connects via WebSocket (token as Sec-WebSocket-Protocol)', async () => {
        const peerToken = api.getToken(PEER_WALLET);
        peerWS = new ChatWS(srv.wsBase, roomId, peerToken, PEER_WALLET, peerKeys.pub, peerKeys.priv, clientKeys.pub);
        await peerWS.connect();
      });

      await t.run('client closes chat', async () => {
        const r = await api.closeChat(roomId, CLIENT_WALLET);
        assertStatus(r, 200, 'client close');
        await sleep(500);
      });

      await t.run('peer WS reaches terminal state (not infinite reconnect)', async () => {
        const result = await peerWS.reconnectUntilTerminal(api);
        if (!result.terminal) throw new Error('Expected terminal, got reconnected');
        if (result.status !== 'closed') throw new Error(`Expected status=closed, got ${result.status}`);
      });

      await t.run('UI contract: peer should show closed screen after terminal', async () => {
        const roomR = await api.getChatRoom(roomId, PEER_WALLET);
        const shouldReconnect = roomR.body.status === 'active';
        const showClosed = roomR.body.status !== 'active';
        if (shouldReconnect) throw new Error('shouldReconnect=true for closed room');
        if (!showClosed) throw new Error('showClosed=false for closed room');
      });

      peerWS?.close();

    } finally { await srv.stop(); }
  }

  // ── Test B: Peer closes → client gets peer_left event, room stays open ──
  {
    const srv = new TestServer();
    try {
      await srv.start();
      const api = new ApiClient(srv.base);
      const { clientKeys, peerKeys, roomId } = await setupChat(api);

      let clientWS;

      await t.run('client connects via WebSocket', async () => {
        const clientToken = api.getToken(CLIENT_WALLET);
        clientWS = new ChatWS(srv.wsBase, roomId, clientToken, CLIENT_WALLET, clientKeys.pub, clientKeys.priv, peerKeys.pub);
        await clientWS.connect();
      });

      await t.run('peer closes chat → 200 peer_left', async () => {
        const r = await api.closeChat(roomId, PEER_WALLET);
        assertStatus(r, 200, 'peer close');
        if (r.body.status !== 'peer_left') throw new Error(`Expected status=peer_left, got ${r.body.status}`);
      });

      await t.run('client WS receives peer_left system event', async () => {
        await clientWS.waitForSystemEvent('peer_left', 5000);
      });

      await t.run('room status is peer_left (not closed)', async () => {
        const r = await api.getChatRoom(roomId, CLIENT_WALLET);
        if (r.body.status !== 'peer_left') throw new Error(`Expected peer_left, got ${r.body.status}`);
      });

      await t.run('client can still close the room after peer left', async () => {
        const r = await api.closeChat(roomId, CLIENT_WALLET);
        assertStatus(r, 200, 'client close after peer left');
        if (r.body.status !== 'closed') throw new Error(`Expected status=closed, got ${r.body.status}`);
      });

      clientWS?.close();

    } finally { await srv.stop(); }
  }

  // ── Test C: Cannot send to closed room ───────────────────────────────
  {
    const srv = new TestServer();
    try {
      await srv.start();
      const api = new ApiClient(srv.base);
      const { clientKeys, peerKeys, roomId } = await setupChat(api);

      await api.closeChat(roomId, CLIENT_WALLET);

      await t.run('poll send rejected for closed room (410)', async () => {
        const enc = encrypt('late message', clientKeys.priv, peerKeys.pub);
        const r = await api.pollSend(roomId, CLIENT_WALLET, clientKeys.pub, enc.nonce, enc.ciphertext);
        assertStatus(r, 410, 'send to closed room');
      });

      await t.run('double close rejected (410)', async () => {
        const r = await api.closeChat(roomId, PEER_WALLET);
        assertStatus(r, 410, 'double close');
      });

    } finally { await srv.stop(); }
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
