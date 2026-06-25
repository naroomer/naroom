// 014_reputation.js — peer reputation accumulates and is visible to client
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_WALLET   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

// Force-close any active/pending listing so the next createListing can succeed
function clearListings(srv) {
  // wallet_sessions uses wallet_hash PK (no wallet_address column) — filter by is_sample only
  srv.db(`UPDATE listings SET status='closed' WHERE is_sample=0 AND status IN ('active','pending','matched')`);
}

// Run a full session: create listing → respond → accept → open chat → client closes → return review_token
async function fullSession(api, srv) {
  clearListings(srv);
  const clientKeys = newKeypair();
  const peerKeys   = newKeypair();

  const cr = await api.createListing(CLIENT_WALLET);
  if (cr.status !== 201) throw new Error(`createListing: ${cr.status} ${JSON.stringify(cr.body)}`);
  const listingId = cr.body.listing_id;

  await pollUntil(async () => {
    const r = await api.getListing(listingId);
    return r.body.status === 'active' ? true : null;
  }, { timeout: 45000, label: 'listing active' });

  const peerResp = await api.respond(listingId, PEER_WALLET, peerKeys.pub);
  if (peerResp.status !== 201) throw new Error(`respond: ${peerResp.status}`);

  const rr = await api.getResponses(listingId, CLIENT_WALLET);
  await api.acceptResponse(rr.body[0].id, CLIENT_WALLET, clientKeys.pub);

  const room = await pollUntil(async () => {
    const r = await api.getPeerChatroom(PEER_WALLET, listingId);
    return r.status === 200 ? r.body : null;
  }, { timeout: 45000, label: 'chat room open' });

  const closeRes = await api.closeChat(room.room_id, CLIENT_WALLET);
  clearListings(srv); // restore to clean state for next test
  return closeRes.body.review_token ?? null;
}

// Create a listing, respond, get reputation from responses, then clean up
async function fetchReputation(api, srv) {
  clearListings(srv);
  const peerKeys = newKeypair();
  const clientKeys = newKeypair();

  const cr = await api.createListing(CLIENT_WALLET);
  if (cr.status !== 201) throw new Error(`createListing: ${cr.status} ${JSON.stringify(cr.body)}`);
  const listingId = cr.body.listing_id;

  await pollUntil(async () => {
    const r = await api.getListing(listingId);
    return r.body.status === 'active' ? true : null;
  }, { timeout: 45000, label: 'listing active for rep fetch' });

  const peerResp = await api.respond(listingId, PEER_WALLET, peerKeys.pub);
  if (peerResp.status !== 201) throw new Error(`respond: ${peerResp.status}`);

  const rr = await api.getResponses(listingId, CLIENT_WALLET);
  if (!rr.body.length) throw new Error('no responses found');

  clearListings(srv);
  return rr.body[0].reputation;
}

export async function run() {
  console.log('\n=== 014: Peer Reputation ===');
  const srv = new TestServer();
  const t = new Runner('014_reputation');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
    await api.verifyWallet(PEER_WALLET, 'BTC', 'peer');

    // ── Initial state checks ─────────────────────────────────────────────────

    await t.run('responses API includes reputation field with correct types', async () => {
      const rep = await fetchReputation(api, srv);
      if (!rep) throw new Error('reputation field missing from response');
      if (typeof rep.sessions_completed !== 'number') throw new Error('sessions_completed not a number');
      if (typeof rep.thumbs_up !== 'number') throw new Error('thumbs_up not a number');
      if (typeof rep.thumbs_down !== 'number') throw new Error('thumbs_down not a number');
      if (typeof rep.balance_tier !== 'number') throw new Error('balance_tier not a number');
      if (typeof rep.is_new !== 'boolean') throw new Error('is_new not a boolean');
      if (typeof rep.member_since !== 'number') throw new Error('member_since not a number');
    });

    await t.run('new peer: is_new=true, 0 sessions, 0 ratings', async () => {
      const rep = await fetchReputation(api, srv);
      if (!rep.is_new) throw new Error(`is_new=${rep.is_new}, expected true`);
      if (rep.sessions_completed !== 0) throw new Error(`sessions_completed=${rep.sessions_completed}, expected 0`);
      if (rep.thumbs_up !== 0) throw new Error(`thumbs_up=${rep.thumbs_up}, expected 0`);
      if (rep.thumbs_down !== 0) throw new Error(`thumbs_down=${rep.thumbs_down}, expected 0`);
    });

    await t.run('new peer: balance_tier=1 ($1000 set on verify)', async () => {
      const rep = await fetchReputation(api, srv);
      if (rep.balance_tier !== 1) throw new Error(`balance_tier=${rep.balance_tier}, expected 1`);
    });

    await t.run('member_since is a valid recent timestamp', async () => {
      const rep = await fetchReputation(api, srv);
      const now = Math.floor(Date.now() / 1000);
      if (!rep.member_since || rep.member_since < now - 120) throw new Error(`member_since invalid: ${rep.member_since}`);
    });

    // ── Session counters ─────────────────────────────────────────────────────

    await t.run('after 1st session: sessions_completed=1', async () => {
      await fullSession(api, srv);
      const hash = srv.db(`SELECT counselor_hash FROM reputation LIMIT 1`);
      const sc = parseInt(srv.db(`SELECT sessions_completed FROM reputation WHERE counselor_hash='${hash}'`), 10);
      if (sc !== 1) throw new Error(`sessions_completed=${sc}, expected 1`);
    });

    await t.run('thumbs-up review increments thumbs_up', async () => {
      const token = await fullSession(api, srv);
      if (!token) throw new Error('no review_token');
      await api.submitReview(token, 'up');
      const hash = srv.db(`SELECT counselor_hash FROM reputation LIMIT 1`);
      const up = parseInt(srv.db(`SELECT thumbs_up FROM reputation WHERE counselor_hash='${hash}'`), 10);
      if (up < 1) throw new Error(`thumbs_up=${up}, expected >=1`);
    });

    await t.run('thumbs-down review increments thumbs_down', async () => {
      const token = await fullSession(api, srv);
      if (!token) throw new Error('no review_token');
      await api.submitReview(token, 'down');
      const hash = srv.db(`SELECT counselor_hash FROM reputation LIMIT 1`);
      const down = parseInt(srv.db(`SELECT thumbs_down FROM reputation WHERE counselor_hash='${hash}'`), 10);
      if (down < 1) throw new Error(`thumbs_down=${down}, expected >=1`);
    });

    await t.run('API response matches DB session count', async () => {
      const hash = srv.db(`SELECT counselor_hash FROM reputation LIMIT 1`);
      const dbCount = parseInt(srv.db(`SELECT sessions_completed FROM reputation WHERE counselor_hash='${hash}'`), 10);
      const rep = await fetchReputation(api, srv);
      if (rep.sessions_completed !== dbCount) {
        throw new Error(`API sessions_completed=${rep.sessions_completed}, DB=${dbCount}`);
      }
    });

    // ── After 5 sessions: is_new flips ───────────────────────────────────────

    await t.run('after 5+ sessions: is_new=false in API', async () => {
      const hash = srv.db(`SELECT counselor_hash FROM reputation LIMIT 1`);
      let sc = parseInt(srv.db(`SELECT sessions_completed FROM reputation WHERE counselor_hash='${hash}'`), 10);
      while (sc < 5) {
        await fullSession(api, srv);
        sc = parseInt(srv.db(`SELECT sessions_completed FROM reputation WHERE counselor_hash='${hash}'`), 10);
      }
      const rep = await fetchReputation(api, srv);
      if (rep.is_new !== false) throw new Error(`is_new=${rep.is_new}, expected false after 5+ sessions`);
    });

    // ── Balance tier via DB injection ─────────────────────────────────────────

    await t.run('balance_tier: $2500 → tier=2 in API', async () => {
      // wallet_sessions uses wallet_hash PK; filter by role since only one peer in this DB
      srv.db(`UPDATE wallet_sessions SET balance_usd=2500 WHERE role='peer'`);
      const rep = await fetchReputation(api, srv);
      if (rep.balance_tier !== 2) throw new Error(`balance_tier=${rep.balance_tier}, expected 2`);
    });

    await t.run('balance_tier: $3100 → tier=3 in API', async () => {
      srv.db(`UPDATE wallet_sessions SET balance_usd=3100 WHERE role='peer'`);
      const rep = await fetchReputation(api, srv);
      if (rep.balance_tier !== 3) throw new Error(`balance_tier=${rep.balance_tier}, expected 3`);
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
