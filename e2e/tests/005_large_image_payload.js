// 005_large_image_payload.js — body size limits: images pass, oversized JSON rejected
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair, encrypt, fakeImageDataUrl } from '../lib/crypto.js';
import { assertStatus, pollUntil } from '../lib/assert.js';
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
  await api.acceptResponse(rr.body[0].id, CLIENT_WALLET, clientKeys.pub);

  const room = await pollUntil(async () => {
    const r = await api.getPeerChatroom(PEER_WALLET, listingId);
    return r.status === 200 ? r.body : null;
  }, { timeout: 45000, label: 'chat room' });

  return { clientKeys, peerKeys, roomId: room.room_id };
}

export async function run() {
  console.log('\n=== 005: Large Image Payload ===');
  const srv = new TestServer();
  const t = new Runner('005_large_image_payload');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);
    const { clientKeys, peerKeys, roomId } = await setupChat(api);

    await t.run('small text message (< 64KB) accepted', async () => {
      const enc = encrypt('small text', clientKeys.priv, peerKeys.pub);
      const r = await api.pollSend(roomId, CLIENT_WALLET, clientKeys.pub, enc.nonce, enc.ciphertext, 'text');
      assertStatus(r, 201, 'small text');
    });

    await t.run('100KB image accepted via chat/poll/send', async () => {
      const img = fakeImageDataUrl(100_000);
      const enc = encrypt(img, clientKeys.priv, peerKeys.pub);
      const r = await api.pollSend(roomId, CLIENT_WALLET, clientKeys.pub, enc.nonce, enc.ciphertext, 'image_file');
      assertStatus(r, 201, '100KB image');
    });

    await t.run('300KB image_camera accepted via chat/poll/send', async () => {
      const img = fakeImageDataUrl(300_000);
      const enc = encrypt(img, clientKeys.priv, peerKeys.pub);
      const r = await api.pollSend(roomId, CLIENT_WALLET, clientKeys.pub, enc.nonce, enc.ciphertext, 'image_camera');
      assertStatus(r, 201, '300KB image_camera');
    });

    await t.run('300KB image in history for peer', async () => {
      const r = await api.pollReceive(roomId, PEER_WALLET, peerKeys.pub, 0);
      assertStatus(r, 200, 'poll receive');
      const imgs = r.body.messages.filter(m => m.msg_type === 'image_camera');
      if (imgs.length < 1) throw new Error('image_camera not in peer history');
    });

    await t.run('oversized JSON on listing create route rejected (>64KB body)', async () => {
      const clientToken = api.getToken(CLIENT_WALLET);
      const padding = 'x'.repeat(70_000);
      const r = await fetch(`${srv.base}/listing/create`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${clientToken}`,
        },
        body: JSON.stringify({
          city: 'new_york', dependency_type: 'alcohol', help_type: 'crisis',
          urgency: 'urgent', languages: ['en'], currency: 'BTC', padding,
        }),
      });
      if (r.status === 201) throw new Error('Oversized body accepted on listing/create');
    });

    await t.run('msg_type defaults to text for unknown types', async () => {
      const enc = encrypt('test', clientKeys.priv, peerKeys.pub);
      const r = await api.pollSend(roomId, CLIENT_WALLET, clientKeys.pub, enc.nonce, enc.ciphertext, 'unknown_type');
      assertStatus(r, 201, 'unknown msg_type');
      const recv = await api.pollReceive(roomId, PEER_WALLET, peerKeys.pub, 0);
      const last = recv.body.messages.at(-1);
      if (last.msg_type !== 'text') throw new Error(`Expected msg_type=text, got ${last.msg_type}`);
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
