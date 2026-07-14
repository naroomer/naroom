// 044_principal_takeover.js — principal authorization model correctness
//
// Tests:
//   PT-1:  /session/init + /wallet/register; /session/status reflects wallet_linked
//   PT-2:  Same public wallet address → two independent principals (S1, S2)
//   PT-3:  S2 denied listing created by S1 (responses, renew)
//   PT-4:  S2 denied /listing/:id/invoice for S1 listing
//   PT-5:  S2 denied Telegram binding for S1 listing
//   PT-6:  S2 /resume sees no S1 resources (404)
//   PT-7:  S2 denied GET /chat/:id for injected S1 room
//   PT-8:  S2 denied POST /chat/:id/pubkey for S1 room
//   PT-9:  S2 denied POST /chat/:id/close for S1 room
//   PT-10: Legacy listing (owner_principal_id NULL) → 403 from any caller
//   PT-11: Legacy chat room (client/counselor principal_id NULL) → 403 from any caller
//   PT-12: Concurrent /session/recover with same code → exactly one 200, one 401
//   PT-13: /session/recover response includes role field
//   PT-14: Role guards still enforced (client-only, peer-only endpoints)

import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { ChatWS } from '../lib/ws.js';
import { Runner } from '../lib/runner.js';
import WebSocket from 'ws';

const SHARED_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf'; // S1 and S2 share this address
const PEER_WALLET   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

async function sessionInit(base, role) {
  const r = await fetch(`${base}/session/init`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ role }),
  });
  if (r.status !== 201) throw new Error(`/session/init returned ${r.status}`);
  return r.json();
}

async function walletRegister(base, token, wallet, role) {
  const r = await fetch(`${base}/wallet/register`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
    body: JSON.stringify({ wallet_address: wallet, currency: 'BTC', role }),
  });
  return r;
}

async function sessionStatus(base, token) {
  const r = await fetch(`${base}/session/status`, {
    headers: { 'Authorization': `Bearer ${token}` },
  });
  if (!r.ok) throw new Error(`/session/status ${r.status}`);
  return r.json();
}

export async function run() {
  console.log('\n=== 044: Principal Takeover & Authorization ===');
  const srv = new TestServer({ devMode: true });
  const t = new Runner('044_principal_takeover');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    // ── PT-1: session/init + wallet/register ────────────────────────────────
    let s1Token, s1RecoveryCode, s1PrincipalID;
    await t.run('PT-1: /session/init returns token + recovery_code; /session/status reflects wallet_linked', async () => {
      const initData = await sessionInit(srv.base, 'client');
      s1Token = initData.session_token;
      s1RecoveryCode = initData.recovery_code;
      if (!s1Token) throw new Error('No session_token');
      if (!s1RecoveryCode) throw new Error('No recovery_code');

      // Before wallet/register: wallet_linked = false
      const sb = await sessionStatus(srv.base, s1Token);
      if (sb.wallet_linked !== false) throw new Error(`Expected wallet_linked=false, got ${JSON.stringify(sb)}`);
      if (!sb.principal_id) throw new Error('Missing principal_id in /session/status');
      s1PrincipalID = sb.principal_id;

      // Link wallet
      const rr = await walletRegister(srv.base, s1Token, SHARED_WALLET, 'client');
      if (rr.status !== 200) throw new Error(`/wallet/register ${rr.status}: ${await rr.text()}`);
      const rb = await rr.json();
      if (!rb.wallet_linked) throw new Error('wallet_linked not true after register');

      // After: wallet_linked = true
      const sb2 = await sessionStatus(srv.base, s1Token);
      if (sb2.wallet_linked !== true) throw new Error(`Expected wallet_linked=true, got ${JSON.stringify(sb2)}`);
    });

    // ── PT-2: Two independent principals for the SAME wallet address ─────────
    let s2Token, s2PrincipalID;
    await t.run('PT-2: Same wallet address → two independent principals S1 and S2', async () => {
      // S2: fresh /session/init → completely new principal
      const initData2 = await sessionInit(srv.base, 'client');
      s2Token = initData2.session_token;
      if (!s2Token) throw new Error('No session_token for S2');

      // Get S2 principal_id BEFORE linking wallet
      const sb2 = await sessionStatus(srv.base, s2Token);
      s2PrincipalID = sb2.principal_id;
      if (!s2PrincipalID) throw new Error('No principal_id for S2');

      // Verify S1 and S2 have distinct principal_ids
      if (s1PrincipalID === s2PrincipalID) throw new Error('S1 and S2 share the same principal_id');

      // S2 also links the SAME shared wallet (idempotent at the DB level — wallet_hash is a billing tag)
      const rr = await walletRegister(srv.base, s2Token, SHARED_WALLET, 'client');
      if (rr.status !== 200) throw new Error(`S2 /wallet/register ${rr.status}: ${await rr.text()}`);
    });

    // Store S1 token in api client
    api.tokens[SHARED_WALLET] = { token: s1Token, role: 'client' };

    // ── S1 creates a listing ─────────────────────────────────────────────────
    let listingId;
    await t.run('S1 creates a listing', async () => {
      const r = await api.createListing(SHARED_WALLET);
      if (r.status !== 201) throw new Error(`create listing ${r.status}: ${JSON.stringify(r.body)}`);
      listingId = r.body.listing_id;
      if (!listingId) throw new Error('No listing_id');
    });

    // ── PT-3: S2 denied S1 listing resources ────────────────────────────────
    await t.run('PT-3a: S2 cannot view S1 listing responses (403)', async () => {
      const r = await fetch(`${srv.base}/listing/${listingId}/responses`, {
        headers: { 'Authorization': `Bearer ${s2Token}` },
      });
      if (r.status !== 403) throw new Error(`Expected 403, got ${r.status}`);
    });

    await t.run('PT-3b: S2 cannot renew S1 listing (403)', async () => {
      const r = await fetch(`${srv.base}/listing/${listingId}/renew`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${s2Token}` },
      });
      if (r.status !== 403) throw new Error(`Expected 403, got ${r.status}`);
    });

    // ── PT-4: S2 denied S1 invoice ──────────────────────────────────────────
    await t.run('PT-4: S2 cannot view S1 listing invoice (not 200)', async () => {
      const r = await fetch(`${srv.base}/listing/${listingId}/invoice`, {
        headers: { 'Authorization': `Bearer ${s2Token}` },
      });
      // Must not be 200 (S2 is not the owner)
      if (r.status === 200) throw new Error(`S2 should not see S1 invoice, got 200`);
    });

    // ── PT-5: S2 denied Telegram binding of S1 listing ──────────────────────
    await t.run('PT-5: S2 cannot bind Telegram to S1 listing (403)', async () => {
      const r = await fetch(`${srv.base}/telegram/client/token`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${s2Token}` },
        body: JSON.stringify({ listing_id: listingId }),
      });
      if (r.status !== 403) throw new Error(`Expected 403, got ${r.status}`);
    });

    // ── PT-6: S2 /resume returns 404 (no S1 resources visible) ──────────────
    await t.run('PT-6: S2 /resume sees no S1 resources (404)', async () => {
      const r = await fetch(`${srv.base}/resume`, {
        headers: { 'Authorization': `Bearer ${s2Token}` },
      });
      if (r.status !== 404) throw new Error(`Expected 404, got ${r.status}`);
    });

    // ── Inject a chat room owned by S1 ──────────────────────────────────────
    const clientKeys = newKeypair();
    const peerKeys   = newKeypair();
    let chatRoomId;
    await t.run('Inject S1 chat room for takeover tests', async () => {
      await api.verifyWallet(PEER_WALLET, 'BTC', 'peer');

      // Get peer principal_id via /session/status
      const peerToken = api.tokens[PEER_WALLET]?.token;
      if (!peerToken) throw new Error('No peer token');
      const peerStatus = await sessionStatus(srv.base, peerToken);
      const peerPrincipalID = peerStatus.principal_id;
      if (!peerPrincipalID) throw new Error('No peer principal_id');

      // Get wallet hashes from DB for the chat room's hash fields
      const clientWalletHash   = srv.db(`SELECT wallet_hash FROM principals WHERE id = '${s1PrincipalID}'`);
      const peerWalletHash     = srv.db(`SELECT wallet_hash FROM principals WHERE id = '${peerPrincipalID}'`);

      chatRoomId = 'room_takeover_' + Date.now();
      srv.db(`
        INSERT INTO chat_rooms (id, listing_id, response_id, client_hash, counselor_hash,
          client_pubkey, counselor_pubkey, started_at, expires_at, status,
          client_principal_id, counselor_principal_id)
        VALUES (
          '${chatRoomId}', '${listingId}', 'rsp_test_044',
          '${clientWalletHash}', '${peerWalletHash}',
          '${clientKeys.pub}', '${peerKeys.pub}',
          ${Math.floor(Date.now()/1000) - 60},
          ${Math.floor(Date.now()/1000) + 3600},
          'active', '${s1PrincipalID}', '${peerPrincipalID}'
        )
      `);
    });

    // ── PT-7: S2 denied chat metadata ───────────────────────────────────────
    await t.run('PT-7: S2 cannot GET S1 chat room metadata (403)', async () => {
      const r = await fetch(`${srv.base}/chat/${chatRoomId}`, {
        headers: { 'Authorization': `Bearer ${s2Token}` },
      });
      if (r.status !== 403) throw new Error(`Expected 403, got ${r.status}`);
    });

    // ── PT-8: S2 denied pubkey update ───────────────────────────────────────
    await t.run('PT-8: S2 cannot update pubkey in S1 chat room (403)', async () => {
      const r = await fetch(`${srv.base}/chat/${chatRoomId}/pubkey`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${s2Token}` },
        body: JSON.stringify({ pubkey: peerKeys.pub }),
      });
      if (r.status !== 403) throw new Error(`Expected 403, got ${r.status}`);
    });

    // ── PT-9: S2 denied chat close ──────────────────────────────────────────
    await t.run('PT-9: S2 cannot close S1 chat room (403)', async () => {
      const r = await fetch(`${srv.base}/chat/${chatRoomId}/close`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${s2Token}` },
      });
      if (r.status !== 403) throw new Error(`Expected 403, got ${r.status}`);
    });

    // ── PT-10: Legacy listing (owner_principal_id NULL) → 403 ───────────────
    await t.run('PT-10: Legacy listing (owner_principal_id NULL) fails closed (403)', async () => {
      const legacyListingId = 'lst_legacy_044_' + Date.now();
      const anyWalletHash = srv.db(`SELECT wallet_hash FROM principals WHERE id = '${s1PrincipalID}'`);
      srv.db(`
        INSERT INTO listings (id, city, dependency_type, help_type, urgency, languages, wallet_hash,
          created_at, visible_until, status, owner_principal_id)
        VALUES ('${legacyListingId}', 'tbilisi', 'alcohol', 'crisis', 'urgent', '["en"]', '${anyWalletHash}',
          ${Math.floor(Date.now()/1000)}, ${Math.floor(Date.now()/1000) + 3600},
          'active', NULL)
      `);
      // Even S1 (which owns the wallet_hash) must get 403 — no principal_id means fail closed
      const r = await fetch(`${srv.base}/listing/${legacyListingId}/responses`, {
        headers: { 'Authorization': `Bearer ${s1Token}` },
      });
      if (r.status !== 403) throw new Error(`Legacy listing should fail closed (403), got ${r.status}`);
    });

    // ── PT-11: Legacy chat room (NULL principal_ids) → 403 ──────────────────
    await t.run('PT-11: Legacy chat room (NULL principal_ids) fails closed (403)', async () => {
      const legacyRoomId = 'room_legacy_044_' + Date.now();
      const anyHash = srv.db(`SELECT wallet_hash FROM principals WHERE id = '${s1PrincipalID}'`);
      srv.db(`
        INSERT INTO chat_rooms (id, listing_id, response_id, client_hash, counselor_hash,
          client_pubkey, counselor_pubkey, started_at, expires_at, status,
          client_principal_id, counselor_principal_id)
        VALUES ('${legacyRoomId}', 'lst_x', 'rsp_x',
          '${anyHash}', '${anyHash}',
          '${clientKeys.pub}', '${peerKeys.pub}',
          ${Math.floor(Date.now()/1000) - 60},
          ${Math.floor(Date.now()/1000) + 3600},
          'active', NULL, NULL)
      `);
      // GET metadata: 403 (fail closed)
      const r1 = await fetch(`${srv.base}/chat/${legacyRoomId}`, {
        headers: { 'Authorization': `Bearer ${s1Token}` },
      });
      if (r1.status !== 403) throw new Error(`Legacy room GET should be 403, got ${r1.status}`);

      // Close: 403 (fail closed)
      const r2 = await fetch(`${srv.base}/chat/${legacyRoomId}/close`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${s1Token}` },
      });
      if (r2.status !== 403) throw new Error(`Legacy room close should be 403, got ${r2.status}`);
    });

    // ── PT-12: Concurrent /session/recover → exactly one 200, one 401 ────────
    await t.run('PT-12: Concurrent /session/recover with same code → exactly one 200, one 401', async () => {
      const freshInit = await sessionInit(srv.base, 'client');
      const freshCode = freshInit.recovery_code;
      if (!freshCode) throw new Error('No recovery_code from fresh /session/init');

      const [r1, r2] = await Promise.all([
        fetch(`${srv.base}/session/recover`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ recovery_code: freshCode }),
        }),
        fetch(`${srv.base}/session/recover`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ recovery_code: freshCode }),
        }),
      ]);

      const statuses = [r1.status, r2.status].sort();
      if (statuses[0] !== 200 || statuses[1] !== 401) {
        throw new Error(`Expected [200, 401] from concurrent recover, got ${statuses}`);
      }
    });

    // ── PT-13: /session/recover response includes role field ──────────────────
    await t.run('PT-13: /session/recover response includes role field', async () => {
      const freshInit = await sessionInit(srv.base, 'client');
      const freshCode = freshInit.recovery_code;

      const r = await fetch(`${srv.base}/session/recover`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ recovery_code: freshCode }),
      });
      if (r.status !== 200) throw new Error(`/session/recover ${r.status}`);
      const body = await r.json();
      if (!body.role) throw new Error(`/session/recover missing role: ${JSON.stringify(body)}`);
      if (body.role !== 'client') throw new Error(`Expected role=client, got ${body.role}`);
      if (!body.session_token) throw new Error('Missing session_token');
      if (!body.recovery_code) throw new Error('Missing new recovery_code');
    });

    // ── PT-14: Role guards ───────────────────────────────────────────────────
    await t.run('PT-14a: client session rejected on /peer/region (403)', async () => {
      const r = await fetch(`${srv.base}/peer/region`, {
        headers: { 'Authorization': `Bearer ${s1Token}` },
      });
      if (r.status !== 403) throw new Error(`Expected 403, got ${r.status}`);
    });

    await t.run('PT-14b: peer session rejected on /listing/create (403)', async () => {
      const peerToken = api.tokens[PEER_WALLET]?.token;
      if (!peerToken) throw new Error('No peer token');
      const r = await fetch(`${srv.base}/listing/create`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${peerToken}` },
        body: JSON.stringify({ city: 'tbilisi', dependency_type: 'alcohol', help_type: 'crisis', urgency: 'urgent', languages: ['en'], currency: 'BTC' }),
      });
      if (r.status !== 403) throw new Error(`Expected 403, got ${r.status}`);
    });

    // ── PT-15: S2 WebSocket rejected on S1's room; S1 stays connected ───────
    await t.run('PT-15: S2 WebSocket to S1 room rejected (403) while S1 remains connected', async () => {
      // S1 connects to the chat room — S1 is the client_principal_id
      const s1WS = new ChatWS(srv.wsBase, chatRoomId, s1Token, SHARED_WALLET,
        clientKeys.pub, clientKeys.priv, peerKeys.pub);
      await s1WS.connect();

      // S2 attempts to connect to the same room with a different principal
      let s2StatusCode = 0;
      await new Promise((resolve) => {
        const wsUrl = `${srv.wsBase}/chat/ws?room_id=${chatRoomId}`;
        const ws2 = new WebSocket(wsUrl, [s2Token]);
        ws2.on('unexpected-response', (req, res) => {
          s2StatusCode = res.statusCode;
          ws2.terminate();
          resolve();
        });
        ws2.on('open', () => {
          // Connected — wait for server to send close frame or rejection
          ws2.on('close', (code) => {
            s2StatusCode = code >= 4000 ? code : 0;
            resolve();
          });
          setTimeout(() => { ws2.terminate(); resolve(); }, 2000);
        });
        ws2.on('error', () => resolve());
        setTimeout(resolve, 5000);
      });

      if (s2StatusCode !== 403) {
        throw new Error(`Expected S2 WS to be rejected with HTTP 403, got ${s2StatusCode}`);
      }

      // S1 must still be connected and usable
      if (!s1WS.ws || s1WS.ws.readyState !== WebSocket.OPEN) {
        throw new Error('S1 WebSocket was disconnected after S2 attempted to join');
      }
      s1WS.close();
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
