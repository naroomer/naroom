// 001_happy_path.js — full E2E: wallet → listing → respond → accept → chat → close → review
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair, encrypt, fakeImageDataUrl } from '../lib/crypto.js';
import { ChatWS } from '../lib/ws.js';
import { assertStatus, assertHasField, assertNoRoom, assertDbCount, pollUntil, pass, sleep } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_WALLET   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

export async function run() {
  console.log('\n=== 001: Happy Path (full E2E) ===');
  const srv = new TestServer();
  const t = new Runner('001_happy_path');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);
    const clientKeys = newKeypair();
    const peerKeys   = newKeypair();
    let listingId, responseId, invoiceId, roomId, reviewToken;

    // ── Phase 1: Wallet verification ─────────────────────────────────────
    await t.run('client wallet verifies + gets session token', async () => {
      const r = await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      assertStatus(r, 200, 'client verify');
      assertHasField(r.body, 'session_token', 'client verify');
    });

    await t.run('peer wallet verifies + gets session token', async () => {
      const r = await api.verifyWallet(PEER_WALLET, 'BTC', 'peer');
      assertStatus(r, 200, 'peer verify');
      assertHasField(r.body, 'session_token', 'peer verify');
    });

    // ── Phase 2: Create listing ──────────────────────────────────────────
    await t.run('client creates listing', async () => {
      const r = await api.createListing(CLIENT_WALLET);
      assertStatus(r, 201, 'create listing');
      assertHasField(r.body, 'listing_id', 'create listing');
      assertHasField(r.body, 'invoice_id', 'create listing');
      listingId = r.body.listing_id;
      invoiceId = r.body.invoice_id;
    });

    await t.run('duplicate listing rejected', async () => {
      const r = await api.createListing(CLIENT_WALLET);
      // Idempotency: pending listing returns same invoice (200), not 409.
      if (r.status === 200) {
        if (r.body.invoice_id !== invoiceId) throw new Error(`Expected same invoice_id, got ${r.body.invoice_id}`);
      } else {
        assertStatus(r, 409, 'duplicate listing');
      }
    });

    await t.run('no session → create listing rejected', async () => {
      const r = await fetch(`${srv.base}/listing/create`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ city: 'london', dependency_type: 'alcohol', help_type: 'crisis', urgency: 'urgent', languages: ['en'], currency: 'BTC' }),
      });
      if (r.status !== 401) throw new Error(`Expected 401 without session, got ${r.status}`);
    });

    // ── Phase 3: Listing activates ───────────────────────────────────────
    await t.run('listing activates after invoice auto-confirm', async () => {
      const listing = await pollUntil(async () => {
        const r = await api.getListing(listingId);
        return r.body.status === 'active' ? r.body : null;
      }, { timeout: 45000, label: 'listing active' });
      if (listing.status !== 'active') throw new Error('Listing not active');
    });

    await t.run('listing appears on board', async () => {
      const r = await api.getBoard('new_york');
      assertStatus(r, 200, 'board');
      const found = r.body.find(l => l.id === listingId);
      if (!found) throw new Error('Listing not on board');
    });

    await t.run('peer cannot see chat room before respond', async () => {
      await assertNoRoom(api, PEER_WALLET, listingId, 'before respond');
    });

    // ── Phase 4: Peer responds ───────────────────────────────────────────
    await t.run('peer responds to listing', async () => {
      const r = await api.respond(listingId, PEER_WALLET, peerKeys.pub);
      assertStatus(r, 201, 'respond');
      assertHasField(r.body, 'response_id', 'respond');
      responseId = r.body.response_id;
    });

    await t.run('peer cannot respond twice', async () => {
      const r = await api.respond(listingId, PEER_WALLET, peerKeys.pub);
      assertStatus(r, 409, 'duplicate respond');
    });

    await t.run('peer cannot see chat room before client accepts', async () => {
      await assertNoRoom(api, PEER_WALLET, listingId, 'before accept');
    });

    // ── Phase 5: Client accepts ──────────────────────────────────────────
    await t.run('client sees responses (no peer_address exposed)', async () => {
      const r = await api.getResponses(listingId, CLIENT_WALLET);
      assertStatus(r, 200, 'get responses');
      const resp = r.body.find(x => x.id === responseId);
      if (!resp) throw new Error('Response not listed');
      if (resp.peer_address !== undefined) throw new Error('peer_address should NOT be in response');
      if (!resp.peer_pubkey) throw new Error('peer_pubkey missing');
    });

    await t.run('stranger cannot see responses', async () => {
      const r = await api.getResponses(listingId, PEER_WALLET);
      assertStatus(r, 403, 'stranger responses');
    });

    await t.run('client accepts response', async () => {
      const r = await api.acceptResponse(responseId, CLIENT_WALLET, clientKeys.pub);
      assertStatus(r, 200, 'accept');
      assertHasField(r.body, 'invoice_id', 'accept');
    });

    await t.run('peer poll returns 404 while invoice not yet confirmed', async () => {
      await assertNoRoom(api, PEER_WALLET, listingId, 'after accept, before invoice');
    });

    // ── Phase 6: Chat room opens ─────────────────────────────────────────
    await t.run('chat room created after peer pays $15', async () => {
      const room = await pollUntil(async () => {
        const r = await api.getPeerChatroom(PEER_WALLET, listingId);
        return r.status === 200 ? r.body : null;
      }, { timeout: 45000, label: 'chat room for peer' });
      roomId = room.room_id;
    });

    await t.run('listing removed from board after chat opens', async () => {
      const r = await api.getBoard('new_york');
      const found = r.body.find(l => l.id === listingId);
      if (found) throw new Error('Listing still on board after match');
    });

    await t.run('DB: room response_id matches current response', async () => {
      const val = srv.db(`SELECT response_id FROM chat_rooms WHERE id='${roomId}'`);
      if (val !== responseId) throw new Error(`room.response_id=${val}, expected=${responseId}`);
    });

    await t.run('client gets room with role=client and my_pubkey', async () => {
      const r = await api.getChatRoom(roomId, CLIENT_WALLET);
      assertStatus(r, 200, 'client getChatRoom');
      if (r.body.role !== 'client') throw new Error(`Expected role=client, got ${r.body.role}`);
      if (r.body.peer_pubkey !== peerKeys.pub) throw new Error('peer_pubkey mismatch');
      if (!r.body.my_pubkey) throw new Error('my_pubkey missing from getChatRoom response');
    });

    await t.run('peer gets room with role=peer and my_pubkey', async () => {
      const r = await api.getChatRoom(roomId, PEER_WALLET);
      assertStatus(r, 200, 'peer getChatRoom');
      if (r.body.role !== 'peer') throw new Error(`Expected role=peer, got ${r.body.role}`);
      if (r.body.peer_pubkey !== clientKeys.pub) throw new Error('peer_pubkey mismatch');
    });

    await t.run('stranger cannot access room', async () => {
      // No session for stranger — must be 401, not 403
      const r = await fetch(`${srv.base}/chat/${roomId}`, { method: 'GET' });
      if (r.status !== 401) throw new Error(`Expected 401 without session, got ${r.status}`);
    });

    // ── Phase 7: WebSocket chat ──────────────────────────────────────────
    let clientWS, peerWS;

    await t.run('both actors connect via WebSocket (token as Sec-WebSocket-Protocol)', async () => {
      const clientToken = api.getToken(CLIENT_WALLET);
      const peerToken   = api.getToken(PEER_WALLET);
      clientWS = new ChatWS(srv.wsBase, roomId, clientToken, CLIENT_WALLET, clientKeys.pub, clientKeys.priv, peerKeys.pub);
      peerWS   = new ChatWS(srv.wsBase, roomId, peerToken,   PEER_WALLET,   peerKeys.pub,   peerKeys.priv,   clientKeys.pub);
      await clientWS.connect();
      await peerWS.connect();
    });

    await t.run('client sends message, peer receives it', async () => {
      const waiter = peerWS.waitForMessage(8000);
      clientWS.send('Hello from client');
      const msg = await waiter;
      if (msg.decrypted !== 'Hello from client') throw new Error(`Decrypted: ${msg.decrypted}`);
      if (msg.sender_pubkey !== clientKeys.pub) throw new Error('Wrong sender');
    });

    await t.run('peer sends reply, client receives it', async () => {
      const waiter = clientWS.waitForMessage(8000);
      peerWS.send('Hello from peer');
      const msg = await waiter;
      if (msg.decrypted !== 'Hello from peer') throw new Error(`Decrypted: ${msg.decrypted}`);
    });

    // ── Phase 7b: Poll-based messaging ───────────────────────────────────
    await t.run('poll send text (client)', async () => {
      const enc = encrypt('poll text from client', clientKeys.priv, peerKeys.pub);
      const r = await api.pollSend(roomId, CLIENT_WALLET, clientKeys.pub, enc.nonce, enc.ciphertext, 'text');
      assertStatus(r, 201, 'poll send');
      assertHasField(r.body, 'id', 'poll send');
    });

    await t.run('poll receive (peer sees messages)', async () => {
      const r = await api.pollReceive(roomId, PEER_WALLET, peerKeys.pub, 0);
      assertStatus(r, 200, 'poll receive');
      if (!r.body.messages || r.body.messages.length < 1) throw new Error('No messages in poll');
    });

    // ── Phase 7c: Image payload ───────────────────────────────────────────
    await t.run('300KB image sends via poll', async () => {
      const img = fakeImageDataUrl(300_000);
      const enc = encrypt(img, clientKeys.priv, peerKeys.pub);
      const r = await api.pollSend(roomId, CLIENT_WALLET, clientKeys.pub, enc.nonce, enc.ciphertext, 'image_camera');
      assertStatus(r, 201, 'image poll send');
    });

    await t.run('image preserved in history (peer reload)', async () => {
      const r = await api.pollReceive(roomId, PEER_WALLET, peerKeys.pub, 0);
      const imgs = r.body.messages.filter(m => m.msg_type === 'image_camera');
      if (imgs.length < 1) throw new Error('Image not in history');
      if (imgs[0].ciphertext.length < 100) throw new Error('Image ciphertext suspiciously small');
    });

    // ── Phase 8: Close chat (symmetric two-step close) ───────────────────
    // Symmetric CloseChat: first close → partial close; second close → full close + review_token.
    // Peer closes first so client (second closer) receives the review_token.
    await t.run('peer closes first → status=peer_left', async () => {
      const r = await api.closeChat(roomId, PEER_WALLET);
      assertStatus(r, 200, 'peer first close');
      if (r.body.status !== 'peer_left') throw new Error(`Expected peer_left, got ${r.body.status}`);
    });

    await t.run('client closes second → full close, receives review_token', async () => {
      const r = await api.closeChat(roomId, CLIENT_WALLET);
      assertStatus(r, 200, 'close chat');
      if (r.body.status !== 'closed') throw new Error('status not closed');
      assertHasField(r.body, 'review_token', 'close by client');
      reviewToken = r.body.review_token;
    });

    await t.run('room is now closed', async () => {
      const r = await api.getChatRoom(roomId, CLIENT_WALLET);
      if (r.body.status !== 'closed') throw new Error(`status=${r.body.status}`);
    });

    await t.run('cannot send to closed room', async () => {
      const enc = encrypt('late msg', clientKeys.priv, peerKeys.pub);
      const r = await api.pollSend(roomId, CLIENT_WALLET, clientKeys.pub, enc.nonce, enc.ciphertext);
      assertStatus(r, 410, 'send to closed room');
    });

    await t.run('peer WS detects closed room (terminal state, stops reconnect)', async () => {
      const result = await peerWS.reconnectUntilTerminal(api);
      if (!result.terminal) throw new Error('Expected terminal state');
    });

    // ── Phase 9: Review ───────────────────────────────────────────────────
    await t.run('client submits review', async () => {
      const r = await api.submitReview(reviewToken, 'up');
      assertStatus(r, 200, 'submit review');
    });

    await t.run('review token cannot be reused', async () => {
      const r = await api.submitReview(reviewToken, 'down');
      if (r.status === 200) throw new Error('Token reuse should be rejected');
    });

    // ── Phase 10: Original listing restored to board after close ─────────
    await t.run('original listing restored to board after chat closed', async () => {
      const r = await api.getListing(listingId);
      if (r.body.status !== 'active') throw new Error(`Listing status=${r.body.status}, expected active`);
      const board = await api.getBoard('new_york');
      const found = board.body.find(l => l.id === listingId);
      if (!found) throw new Error('Restored listing not on board');
    });

    await t.run('client cannot create second listing while original is active', async () => {
      const r2 = await api.createListing(CLIENT_WALLET, 'london');
      assertStatus(r2, 409, 'duplicate listing while original active');
    });

    clientWS?.close();
    peerWS?.close();

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
