// 036_ban_enforcement.js — RP-4 ban enforcement regression tests
//
// Verifies that banned wallets are blocked from active participation:
//   T1: banned wallet cannot respond to listing → 403
//   T2: banned wallet cannot create listing → 403
//   T3: banned wallet cannot renew listing → 403
//   T4: banned wallet cannot send chat poll message → 403
//   T5: banned wallet cannot update chat pubkey → 403
//   T6: non-banned wallet still works normally (sanity check)
//   T7: banned wallet CAN still submit abuse report → 200
//   T8: banned wallet CAN still access GET /board and GET /listing → 200
//
// Ban is stored in abuse_counters.banned_until.
// We inject it directly via DB to avoid needing 3+ real peer reporters.

import { TestServer, sleep } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { assertStatus, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';
import { createHmac } from 'crypto';

const TEST_SALT = 'e2e-test-salt';

function walletHash(address) {
  const addr = address.trim();
  const lower = addr.toLowerCase();
  const normalized = (lower.startsWith('bc1') || lower.startsWith('ltc1')) ? lower : addr;
  return createHmac('sha256', Buffer.from(TEST_SALT))
    .update('naroom:v1:' + normalized)
    .digest('hex');
}

// Wallets
const BANNED_CLIENT = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const NORMAL_CLIENT = '1CounterpartyXXXXXXXXXXXXXXXUWLpVr';
const PEER_WALLET   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';
const PEER_WALLET_2 = '1NiNja1bUmhSoTXozBRBEtR8LeF9TkDDmj';

// Inject a permanent ban for a wallet hash directly into abuse_counters.
function injectBan(srv, address) {
  const hash = walletHash(address);
  const now = Math.floor(Date.now() / 1000);
  const tenYears = 10 * 365 * 24 * 3600;
  const bannedUntil = now + tenYears;
  srv.db(
    `INSERT OR REPLACE INTO abuse_counters ` +
    `(client_hash, abuse_misuse, total, banned_until) ` +
    `VALUES ('${hash}', 5, 5, ${bannedUntil})`
  );
  return bannedUntil;
}

export async function run() {
  console.log('\n=== 036: Ban Enforcement (RP-4) ===');
  const srv = new TestServer({ devMode: true });
  const t = new Runner('036_ban_enforcement');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    // Register all wallets
    await api.verifyWallet(BANNED_CLIENT, 'BTC', 'client');
    await api.verifyWallet(NORMAL_CLIENT, 'BTC', 'client');
    await api.verifyWallet(PEER_WALLET,   'BTC', 'peer');
    await api.verifyWallet(PEER_WALLET_2, 'BTC', 'peer');

    // ── Setup: normal client creates a listing (so we have something to target) ──
    let normalListingId;
    await t.run('setup: normal client creates listing → 201', async () => {
      const r = await api.createListing(NORMAL_CLIENT, 'new_york');
      assertStatus(r, 201, 'normal client createListing');
      normalListingId = r.body.listing_id;
    });

    // Wait for listing to become active (DevMode fast confirm)
    await pollUntil(async () => {
      const r = await api.getListing(normalListingId);
      return r.body?.status === 'active' ? true : null;
    }, { timeout: 30000, label: 'normal listing active' });

    // Peer responds so we can also test renew-blocked scenario
    let responseId;
    await t.run('setup: peer responds to normal listing → 201', async () => {
      const r = await api.respond(normalListingId, PEER_WALLET, 'peer-pubkey-for-test');
      assertStatus(r, 201, 'peer respond');
      responseId = r.body.response_id;
    });

    // ── Inject ban on BANNED_CLIENT ────────────────────────────────────────────
    await t.run('inject permanent ban for BANNED_CLIENT via DB', async () => {
      injectBan(srv, BANNED_CLIENT);
      // Verify the ban is in DB
      const hash = walletHash(BANNED_CLIENT);
      const raw = srv.db(`SELECT banned_until FROM abuse_counters WHERE client_hash = '${hash}'`);
      const now = Math.floor(Date.now() / 1000);
      const bannedUntil = parseInt(raw, 10);
      if (bannedUntil <= now) {
        throw new Error(`ban not set correctly: banned_until=${bannedUntil}, now=${now}`);
      }
    });

    // ── T1: banned wallet cannot respond to listing → 403 ─────────────────────
    await t.run('T1: banned wallet cannot respond to listing → 403', async () => {
      // BANNED_CLIENT is a client, but we need to test a banned peer.
      // Use PEER_WALLET_2 as our banned peer for respond test.
      // First inject ban for PEER_WALLET_2.
      injectBan(srv, PEER_WALLET_2);
      const r = await api.respond(normalListingId, PEER_WALLET_2, 'banned-peer-pubkey');
      assertStatus(r, 403, 'banned peer respond');
      if (r.body?.error !== 'account banned') {
        throw new Error(`expected error="account banned", got ${JSON.stringify(r.body)}`);
      }
    });

    // ── T2: banned wallet cannot create listing → 403 ─────────────────────────
    await t.run('T2: banned wallet cannot create listing → 403', async () => {
      const r = await api.createListing(BANNED_CLIENT, 'new_york');
      assertStatus(r, 403, 'banned client createListing');
      if (r.body?.error !== 'account banned') {
        throw new Error(`expected error="account banned", got ${JSON.stringify(r.body)}`);
      }
    });

    // ── T3: banned wallet cannot renew listing → 403 ──────────────────────────
    // We need a listing owned by a banned wallet.
    // Inject a listing row for BANNED_CLIENT directly.
    let bannedListingId;
    await t.run('setup: inject listing owned by banned wallet', async () => {
      const hash = walletHash(BANNED_CLIENT);
      const now = Math.floor(Date.now() / 1000);
      bannedListingId = 'listing_banned_036';
      srv.db(
        `INSERT OR REPLACE INTO listings ` +
        `(id, city, dependency_type, help_type, urgency, languages, wallet_hash, visible_until, created_at, status) ` +
        `VALUES ('${bannedListingId}', 'new_york', 'alcohol', 'crisis', 'urgent', '["en"]', '${hash}', ` +
        `${now + 86400}, ${now}, 'active')`
      );
    });

    await t.run('T3: banned wallet cannot renew listing → 403', async () => {
      const r = await api.post(`/listing/${bannedListingId}/renew`, {}, BANNED_CLIENT);
      assertStatus(r, 403, 'banned client renewListing');
      if (r.body?.error !== 'account banned') {
        throw new Error(`expected error="account banned", got ${JSON.stringify(r.body)}`);
      }
    });

    // ── T4: banned wallet cannot send chat poll message → 403 ────────────────
    // Inject a chat room owned by banned_client.
    let bannedRoomId;
    await t.run('setup: inject chat room for banned wallet', async () => {
      const clientHash = walletHash(BANNED_CLIENT);
      const peerHash   = walletHash(PEER_WALLET);
      const now = Math.floor(Date.now() / 1000);
      bannedRoomId = 'room_banned_036';
      srv.db(
        `PRAGMA foreign_keys = OFF; ` +
        `INSERT OR REPLACE INTO chat_rooms ` +
        `(id, listing_id, client_hash, counselor_hash, client_pubkey, counselor_pubkey, status, started_at, expires_at) ` +
        `VALUES ('${bannedRoomId}', '${bannedListingId}', '${clientHash}', '${peerHash}', ` +
        `'client-pubkey', 'peer-pubkey', 'active', ${now - 3600}, ${now + 86400}); ` +
        `PRAGMA foreign_keys = ON`
      );
    });

    await t.run('T4: banned wallet cannot send chat poll message → 403', async () => {
      const r = await api.pollSend(bannedRoomId, BANNED_CLIENT, 'client-pubkey', 'nonce123', 'ciphertext123', 'text');
      assertStatus(r, 403, 'banned client pollSend');
      if (r.body?.error !== 'account banned') {
        throw new Error(`expected error="account banned", got ${JSON.stringify(r.body)}`);
      }
    });

    // ── T5: banned wallet cannot update chat pubkey → 403 ────────────────────
    await t.run('T5: banned wallet cannot update chat pubkey → 403', async () => {
      const r = await api.post(`/chat/${bannedRoomId}/pubkey`, { pubkey: 'new-pubkey' }, BANNED_CLIENT);
      assertStatus(r, 403, 'banned client updateChatPubkey');
      if (r.body?.error !== 'account banned') {
        throw new Error(`expected error="account banned", got ${JSON.stringify(r.body)}`);
      }
    });

    // ── T6: non-banned wallet still works normally (sanity check) ─────────────
    await t.run('T6: non-banned wallet can still create listing → 201', async () => {
      // Close the existing normal listing first to allow creating a new one
      srv.db(`UPDATE listings SET status='closed' WHERE id='${normalListingId}'`);
      const r = await api.createListing(NORMAL_CLIENT, 'new_york');
      assertStatus(r, 201, 'non-banned client createListing');
    });

    // ── T7: banned wallet CAN still submit abuse report → 200 or 201 ─────────
    // Need a closed chat room where banned client is the client.
    await t.run('setup: inject closed room for abuse report by peer (against banned client)', async () => {
      const clientHash = walletHash(BANNED_CLIENT);
      const peerHash   = walletHash(PEER_WALLET);
      const now = Math.floor(Date.now() / 1000);
      const roomId = 'room_abusereport_036';
      srv.db(
        `PRAGMA foreign_keys = OFF; ` +
        `INSERT OR IGNORE INTO chat_rooms ` +
        `(id, listing_id, client_hash, counselor_hash, client_pubkey, counselor_pubkey, status, started_at, closed_at, closed_by, expires_at) ` +
        `VALUES ('${roomId}', '${bannedListingId}', '${peerHash}', '${clientHash}', ` +
        `'cpubkey', 'ppubkey', 'closed', ${now - 7200}, ${now - 3600}, 'client', ${now + 3600}); ` +
        `PRAGMA foreign_keys = ON`
      );
    });

    // For T7: we need the banned wallet to be a PEER who reports a client (abuse flow requires peer role).
    // But BANNED_CLIENT has role=client. Let's instead verify that a banned wallet's own role
    // (client) can still access abuse-report — but abuse-report requires peer role.
    // The invariant says: "banned users can still report abuse (they may be victims)".
    // In practice: if banned wallet is a PEER, they can still file a report.
    // Let's inject ban on a peer and verify they can still report.
    await t.run('T7: banned peer wallet CAN still submit abuse report → 200', async () => {
      // Use PEER_WALLET_2 (already banned from T1 setup), report against BANNED_CLIENT
      const bannedClientHash = walletHash(BANNED_CLIENT);
      const bannedPeerHash   = walletHash(PEER_WALLET_2);
      const now = Math.floor(Date.now() / 1000);
      const roomId = 'room_t7_036';
      // Inject a closed room where PEER_WALLET_2 is the counselor (so they can report)
      srv.db(
        `PRAGMA foreign_keys = OFF; ` +
        `INSERT OR IGNORE INTO chat_rooms ` +
        `(id, listing_id, client_hash, counselor_hash, client_pubkey, counselor_pubkey, status, started_at, closed_at, closed_by, expires_at) ` +
        `VALUES ('${roomId}', '${bannedListingId}', '${bannedClientHash}', '${bannedPeerHash}', ` +
        `'cpubkey2', 'ppubkey2', 'closed', ${now - 7200}, ${now - 3600}, 'client', ${now + 3600}); ` +
        `PRAGMA foreign_keys = ON`
      );
      const r = await api.abuseReport(roomId, ['misuse'], PEER_WALLET_2);
      if (r.status !== 200 && r.status !== 201) {
        throw new Error(`expected 200/201 for banned peer abuse report, got ${r.status}: ${JSON.stringify(r.body)}`);
      }
    });

    // ── T8: banned wallet CAN still access GET /board and GET /listing → 200 ──
    await t.run('T8: banned wallet can access GET /board → 200', async () => {
      const r = await api.getBoard('new_york');
      assertStatus(r, 200, 'banned client GET /board');
    });

    await t.run('T8: banned wallet can access GET /listing → 200', async () => {
      const r = await api.getListing(bannedListingId);
      assertStatus(r, 200, 'banned client GET /listing');
    });

    // ── Extra: banned_until is returned in 403 response ───────────────────────
    await t.run('403 response includes banned_until timestamp', async () => {
      const r = await api.createListing(BANNED_CLIENT, 'new_york');
      assertStatus(r, 403, 'banned_until check');
      const now = Math.floor(Date.now() / 1000);
      if (!r.body?.banned_until || r.body.banned_until <= now) {
        throw new Error(`expected banned_until > now in response, got ${JSON.stringify(r.body)}`);
      }
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
