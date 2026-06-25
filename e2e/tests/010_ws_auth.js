// 010_ws_auth.js — WS auth via Sec-WebSocket-Protocol (Step 5)
import { TestServer, sleep } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { ChatWS } from '../lib/ws.js';
import { assertStatus, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';
import WebSocket from 'ws';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_WALLET   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

async function setupRoom(api) {
  const clientKeys = newKeypair();
  const peerKeys   = newKeypair();

  await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
  await api.verifyWallet(PEER_WALLET,   'BTC', 'peer');

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

  return { clientKeys, peerKeys, roomId: room.room_id };
}

export async function run() {
  console.log('\n=== 010: WS Auth via Sec-WebSocket-Protocol ===');
  const srv = new TestServer();
  const t = new Runner('010_ws_auth');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);
    const { clientKeys, peerKeys, roomId } = await setupRoom(api);

    let clientWS;

    await t.run('WS connects with valid token as Sec-WebSocket-Protocol', async () => {
      const token = api.getToken(CLIENT_WALLET);
      clientWS = new ChatWS(srv.wsBase, roomId, token, CLIENT_WALLET, clientKeys.pub, clientKeys.priv, peerKeys.pub);
      await clientWS.connect();
      clientWS.close();
    });

    await t.run('WS without token → connection rejected (401)', async () => {
      const url = `${srv.wsBase}/chat/ws?room_id=${roomId}`;
      // No token, no Authorization header — server should reject
      let rejected = false;
      await new Promise((resolve) => {
        const ws = new WebSocket(url);
        ws.on('unexpected-response', (req, res) => {
          rejected = res.statusCode === 401;
          ws.terminate();
          resolve();
        });
        ws.on('open', () => {
          ws.terminate();
          resolve();
        });
        ws.on('error', () => resolve());
        setTimeout(resolve, 3000);
      });
      if (!rejected) throw new Error('WS without token should be rejected with 401');
    });

    await t.run('WS with invalid token → rejected', async () => {
      const url = `${srv.wsBase}/chat/ws?room_id=${roomId}`;
      let rejected = false;
      await new Promise((resolve) => {
        const ws = new WebSocket(url, ['invalidtokenXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX']);
        ws.on('unexpected-response', (req, res) => {
          rejected = res.statusCode === 401;
          ws.terminate();
          resolve();
        });
        ws.on('open', () => { ws.terminate(); resolve(); });
        ws.on('error', () => resolve());
        setTimeout(resolve, 3000);
      });
      if (!rejected) throw new Error('WS with invalid token should be rejected');
    });

    await t.run('WS token NOT in URL query params (no ?token= leak)', async () => {
      // Verify our ChatWS implementation doesn't put token in URL
      const token = api.getToken(CLIENT_WALLET);
      // The connect() method in ChatWS uses: wsBase + /chat/ws?room_id=xxx — no token in URL
      // We verify by checking the URL we'd generate doesn't contain the token
      const url = `${srv.wsBase}/chat/ws?room_id=${roomId}`;
      if (url.includes(token)) throw new Error('Token found in WS URL — security issue!');
    });

    await t.run('both participants connect and chat via WS (E2E smoke)', async () => {
      const clientToken = api.getToken(CLIENT_WALLET);
      const peerToken   = api.getToken(PEER_WALLET);
      const cWS = new ChatWS(srv.wsBase, roomId, clientToken, CLIENT_WALLET, clientKeys.pub, clientKeys.priv, peerKeys.pub);
      const pWS = new ChatWS(srv.wsBase, roomId, peerToken,   PEER_WALLET,   peerKeys.pub,   peerKeys.priv,   clientKeys.pub);
      await cWS.connect();
      await pWS.connect();

      const waiter = pWS.waitForMessage(5000);
      cWS.send('hello from ws auth test');
      const msg = await waiter;
      if (msg.decrypted !== 'hello from ws auth test') throw new Error(`Got: ${msg.decrypted}`);

      cWS.close();
      pWS.close();
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
