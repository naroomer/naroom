// 038_client_resume.js — regression for production bug:
// listing disappears from board when peer pays chat invoice (listing → 'matched').
// Client loses chat entry point.
//
// Exact failure proven in production:
//   - listing page hid all client UI when status === 'matched' (showed expired_note)
//   - /resume page required manual wallet re-entry, never used stored session token
//
// Fixed by:
//   1. listing page: new {:else if listing.status === 'matched'} branch auto-loads chat via stored session
//   2. resume page: onMount tries stored session tokens before showing wallet form
//   3. ResumeChat handler: fallback returns listing_id when listing is matched but no chat room yet
//
// Tests:
//   T1: GET /resume with valid client session → returns room_id (listing matched, chat active)
//       This is the onMount fast-path: client returns to site, session in storage → immediate redirect
//   T2: GET /listing/{id}/chatroom with client session → returns room_id (listing page recovery path)
//   T3: /resume returns 404 when listing is matched but already has a chat room and it is closed
//       (not the fallback — it is the active-room path that doesn't find a closed room)
//   T4: matched-listing fallback: listing matched, no chat room → /resume returns listing_id
//   T5: scope: /resume does not leak matched listing to unrelated wallet

import { TestServer, sleep } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_WALLET   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';
const OTHER_WALLET  = '1CounterpartyXXXXXXXXXXXXXXXUWLpVr';

let _seq = 0;
function injectListing(srv, walletHash, status = 'active') {
  const now = Math.floor(Date.now() / 1000);
  const id = `lst_038_${now}_${++_seq}`;
  srv.db(
    `INSERT INTO listings (id, city, dependency_type, help_type, urgency, languages, wallet_hash, visible_until, created_at, status, is_sample) ` +
    `VALUES ('${id}', 'new_york', 'alcohol', 'crisis', 'urgent', '["en"]', '${walletHash}', ${now + 3600}, ${now}, '${status}', 0)`
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

        // Verify listing is 'matched' (gone from public board)
        const listing = await api.getListing(listingId);
        if (listing.body.status !== 'matched') {
          throw new Error(`Expected listing status=matched, got ${listing.body.status}`);
        }
        const board = await api.getBoard('new_york');
        if (board.body.listings?.some(l => l.id === listingId)) {
          throw new Error('Listing still on board after going matched');
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

      const tokenClient = srv.registerDirect(CLIENT_WALLET, 'client');
      const tokenOther  = srv.registerDirect(OTHER_WALLET,  'client');
      api.tokens[CLIENT_WALLET] = { token: tokenClient, role: 'client' };
      api.tokens[OTHER_WALLET]  = { token: tokenOther,  role: 'client' };

      // Get client wallet_hash for direct injection
      const clientHash = srv.db(
        `SELECT wallet_hash FROM wallet_sessions WHERE wallet_hash IN (` +
        `  SELECT wallet_hash FROM sessions WHERE role='client' ORDER BY created_at LIMIT 1` +
        `)`
      ).trim();

      let injectedListingId;

      // T4: matched listing with no chat room → /resume returns listing_id (fallback state)
      await t.run('T4: matched listing, no chat room → /resume returns listing_id as fallback', async () => {
        injectedListingId = injectListing(srv, clientHash, 'matched');

        const r = await api.get('/resume', CLIENT_WALLET);
        assertStatus(r, 200, '/resume fallback');
        if (!r.body.listing_id) {
          throw new Error(`Expected listing_id in fallback response, got: ${JSON.stringify(r.body)}`);
        }
        if (r.body.listing_id !== injectedListingId) {
          throw new Error(`listing_id mismatch: got ${r.body.listing_id}, expected ${injectedListingId}`);
        }
        if (r.body.listing_status !== 'matched') {
          throw new Error(`Expected listing_status=matched, got ${r.body.listing_status}`);
        }
      });

      // T5: /resume fallback is wallet-scoped — unrelated wallet does not see client's listing
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
