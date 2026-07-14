// 038_client_resume.js — regression for production bug:
// client loses chat entry point after first peer pays (chat room opened).
//
// New model (2026-07-12): listing stays 'active' when first chat opens. No 'matched' status.
//
// Fixed by:
//   1. listing page: onMount auto-calls /api/listing/{id}/chatroom; renders chat button when room found
//   2. resume page: onMount tries stored session tokens before showing wallet form
//   3. ResumeChat handler: primary: chat_rooms WHERE active; fallback: listing_id for active/expired listing
//
// Tests:
//   T1: GET /resume with valid client session → returns room_id (listing active, first chat open)
//       This is the onMount fast-path: client returns to site, session in storage → immediate redirect
//   T2: GET /listing/{id}/chatroom with client session → returns room_id (listing page recovery path)
//   T3: /resume returns 404 when listing has a closed chat room and no active room
//   T4: active listing, no chat room → /resume returns listing_id (fallback state, new model)
//   T5: scope: /resume does not leak active listing to unrelated wallet

import { TestServer, sleep } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_WALLET   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';
const OTHER_WALLET  = '1CounterpartyXXXXXXXXXXXXXXXUWLpVr';

let _seq = 0;
function injectListing(srv, walletHash, status = 'active', ownerPrincipalID = null) {
  const now = Math.floor(Date.now() / 1000);
  const id = `lst_038_${now}_${++_seq}`;
  const principalCol = ownerPrincipalID ? `, owner_principal_id` : '';
  const principalVal = ownerPrincipalID ? `, '${ownerPrincipalID}'` : '';
  srv.db(
    `INSERT INTO listings (id, city, dependency_type, help_type, urgency, languages, wallet_hash, visible_until, created_at, status, is_sample${principalCol}) ` +
    `VALUES ('${id}', 'new_york', 'alcohol', 'crisis', 'urgent', '["en"]', '${walletHash}', ${now + 3600}, ${now}, '${status}', 0${principalVal})`
  );
  return id;
}

export async function run() {
  console.log('\n=== 038: Client Resume After Listing Matched ===');
  const t = new Runner('038_client_resume');

  // ── T1 + T2: core production bug — listing matched, chat active, client re-auth ─
  // devMode=true: prices seeded, SkipPayments=true → watcher auto-confirms invoices
  {
    const srv = new TestServer({ devMode: true });
    try {
      await srv.start();
      const api = new ApiClient(srv.base);

      await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      await api.verifyWallet(PEER_WALLET,   'BTC', 'peer');

      const clientKeys = newKeypair();
      const peerKeys   = newKeypair();
      let listingId, roomId;

      // Setup: full flow to open a chat room
      await t.run('setup: full flow — listing → peer responds → client accepts → chat opens', async () => {
        const cr = await api.createListing(CLIENT_WALLET);
        assertStatus(cr, 201, 'createListing');
        listingId = cr.body.listing_id;

        await pollUntil(async () => {
          const r = await api.getListing(listingId);
          return r.body.status === 'active' ? true : null;
        }, { timeout: 30000, label: 'listing active' });

        const pr = await api.respond(listingId, PEER_WALLET, peerKeys.pub);
        assertStatus(pr, 201, 'respond');
        const responseId = pr.body.response_id;

        const ar = await api.acceptResponse(responseId, CLIENT_WALLET, clientKeys.pub);
        assertStatus(ar, 200, 'acceptResponse');

        const room = await pollUntil(async () => {
          const r = await api.get('/peer/chatroom?listing_id=' + encodeURIComponent(listingId), PEER_WALLET);
          return r.status === 200 ? r.body : null;
        }, { timeout: 30000, label: 'chat room opened' });
        roomId = room.room_id;

        // In the new model, listing stays 'active' while first chat is open
        // (no 'matched' status). It stays on the board until opened_chats_count reaches 2.
        const listing = await api.getListing(listingId);
        if (listing.body.status !== 'active') {
          throw new Error(`Expected listing status=active (new model), got ${listing.body.status}`);
        }
      });

      // T1: /resume with client session returns room_id (the onMount fast-path in resume page)
      await t.run('T1: GET /resume with client session → room_id (listing matched, chat active)', async () => {
        const r = await api.get('/resume', CLIENT_WALLET);
        assertStatus(r, 200, '/resume with client session');
        if (!r.body.room_id) {
          throw new Error(`Expected room_id, got: ${JSON.stringify(r.body)}`);
        }
        if (r.body.room_id !== roomId) {
          throw new Error(`room_id mismatch: got ${r.body.room_id}, expected ${roomId}`);
        }
      });

      // T2: GET /listing/{id}/chatroom with client session → room_id (listing page recovery path)
      await t.run('T2: GET /listing/{id}/chatroom with client session → room_id', async () => {
        const r = await api.get(`/listing/${listingId}/chatroom`, CLIENT_WALLET);
        assertStatus(r, 200, '/listing/{id}/chatroom');
        if (!r.body.room_id) {
          throw new Error(`Expected room_id, got: ${JSON.stringify(r.body)}`);
        }
        if (r.body.room_id !== roomId) {
          throw new Error(`room_id mismatch: got ${r.body.room_id}, expected ${roomId}`);
        }
      });

    } finally {
      await srv.stop();
    }
  }

  // ── T4 + T5: matched-listing fallback (no chat room yet) ──────────────────
  {
    const srv = new TestServer({ devMode: true });
    try {
      await srv.start();
      const api = new ApiClient(srv.base);

      // Use real API flow so sessions get principal_id and wallet_hash is set on the principal.
      await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      await api.verifyWallet(OTHER_WALLET,  'BTC', 'client');

      // Resolve principal_id for CLIENT_WALLET so we can inject a listing owned by it.
      const statusR = await api.get('/session/status', CLIENT_WALLET);
      if (statusR.status !== 200 || !statusR.body.principal_id) {
        throw new Error(`/session/status failed: ${JSON.stringify(statusR.body)}`);
      }
      const clientPrincipalID = statusR.body.principal_id;

      // Get the wallet_hash for CLIENT_WALLET (needed for listing injection; listings still carry wallet_hash).
      const clientHash = srv.db(
        `SELECT wallet_hash FROM sessions WHERE principal_id='${clientPrincipalID}' LIMIT 1`
      ).trim();

      let injectedListingId;

      // T4: active listing with no chat room → /resume returns listing_id (fallback state)
      // In the new model there is no 'matched' status; listings stay 'active' through all chats.
      await t.run('T4: active listing, no chat room → /resume returns listing_id as fallback', async () => {
        injectedListingId = injectListing(srv, clientHash, 'active', clientPrincipalID);

        const r = await api.get('/resume', CLIENT_WALLET);
        assertStatus(r, 200, '/resume fallback');
        if (!r.body.listing_id) {
          throw new Error(`Expected listing_id in fallback response, got: ${JSON.stringify(r.body)}`);
        }
        if (r.body.listing_id !== injectedListingId) {
          throw new Error(`listing_id mismatch: got ${r.body.listing_id}, expected ${injectedListingId}`);
        }
        if (r.body.listing_status !== 'active') {
          throw new Error(`Expected listing_status=active (new model), got ${r.body.listing_status}`);
        }
      });

      // T5: /resume fallback is principal-scoped — unrelated principal does not see client's listing
      await t.run('T5: /resume matched-listing fallback is wallet-scoped (OTHER_WALLET → 404)', async () => {
        const r = await api.get('/resume', OTHER_WALLET);
        if (r.status !== 404) {
          throw new Error(`Expected 404 for unrelated wallet, got ${r.status} ${JSON.stringify(r.body)}`);
        }
        if (r.body && r.body.listing_id === injectedListingId) {
          throw new Error('Cross-session leak: OTHER_WALLET sees CLIENT_WALLET matched listing');
        }
      });

    } finally {
      await srv.stop();
    }
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
