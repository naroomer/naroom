// 045_principal_contract.js — API-level proof that the principal contract is enforced.
// Simulates the exact call sequence the listing, helper, chat, and recovery screens make.
//
// Tests:
//   PC-1: POST /wallet/register without Authorization → 401 (unauthenticated calls blocked)
//   PC-2: /session/init response includes recovery_code; Bearer /wallet/register → 200
//   PC-3: /session/init → /wallet/register (Bearer) → /listing/create: client listing flow
//   PC-4: /session/init → /wallet/register (Bearer) → /telegram/helper/token: helper flow
//   PC-5: /session/recover returns {session_token, role, recovery_code}; recovered token valid
//   PC-6: Recovered token stored as naroom_session_{role} authorises /resume (not 401)
//   PC-7: GET /listing/{id}/responses without Authorization → 401
//   PC-8: GET /chat/{id} without Authorization → 401
//   PC-9: /session/recover does NOT return a session_token via /wallet/register response

import { TestServer } from '../lib/server.js';
import { Runner } from '../lib/runner.js';

const WALLET      = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_WALLET = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

export async function run() {
  console.log('\n=== 045: Principal Contract Coverage ===');
  const srv = new TestServer({ devMode: true });
  const t = new Runner('045_principal_contract');

  try {
    await srv.start();

    // ── PC-1: /wallet/register without Authorization → 401 ─────────────────
    await t.run('PC-1: POST /wallet/register without Authorization → 401', async () => {
      const r = await fetch(`${srv.base}/wallet/register`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ wallet_address: WALLET, currency: 'BTC', role: 'client' }),
      });
      if (r.status !== 401) throw new Error(`Expected 401, got ${r.status}: ${await r.text()}`);
    });

    // ── PC-2: /session/init → /wallet/register (Bearer) → 200 ──────────────
    let clientToken;
    await t.run('PC-2: /session/init includes recovery_code; Bearer /wallet/register → 200', async () => {
      const initR = await fetch(`${srv.base}/session/init`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ role: 'client' }),
      });
      if (initR.status !== 201) throw new Error(`/session/init ${initR.status}`);
      const initData = await initR.json();
      if (!initData.recovery_code) throw new Error('/session/init missing recovery_code');
      if (!initData.session_token) throw new Error('/session/init missing session_token');
      clientToken = initData.session_token;

      // /wallet/register MUST require Bearer token — prove it with the correct token
      const regR = await fetch(`${srv.base}/wallet/register`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${clientToken}` },
        body: JSON.stringify({ wallet_address: WALLET, currency: 'BTC', role: 'client' }),
      });
      if (regR.status !== 200) throw new Error(`/wallet/register ${regR.status}: ${await regR.text()}`);
      const regData = await regR.json();
      // /wallet/register must NOT return a session_token — token comes from /session/init only
      if (regData.session_token) throw new Error('/wallet/register must not return session_token');
    });

    // ── PC-3: Listing creation through principal model ──────────────────────
    let listingId;
    await t.run('PC-3: /session/init → /wallet/register → /listing/create (client principal flow)', async () => {
      const r = await fetch(`${srv.base}/listing/create`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${clientToken}` },
        body: JSON.stringify({
          city: 'tbilisi', dependency_type: 'alcohol', help_type: 'crisis',
          urgency: 'urgent', languages: ['en'], currency: 'BTC',
        }),
      });
      if (r.status !== 201) throw new Error(`/listing/create ${r.status}: ${await r.text()}`);
      const data = await r.json();
      listingId = data.listing_id;
      if (!listingId) throw new Error('No listing_id in /listing/create response');
    });

    // ── PC-4: Helper (peer) flow ────────────────────────────────────────────
    await t.run('PC-4: Peer /session/init → /wallet/register (Bearer) → /telegram/helper/token', async () => {
      const initR = await fetch(`${srv.base}/session/init`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ role: 'peer' }),
      });
      if (initR.status !== 201) throw new Error(`Peer /session/init ${initR.status}`);
      const initData = await initR.json();
      if (!initData.recovery_code) throw new Error('Peer /session/init missing recovery_code');
      const peerToken = initData.session_token;

      const regR = await fetch(`${srv.base}/wallet/register`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${peerToken}` },
        body: JSON.stringify({ wallet_address: PEER_WALLET, currency: 'BTC', role: 'peer' }),
      });
      if (regR.status !== 200) throw new Error(`Peer /wallet/register ${regR.status}`);

      // Telegram helper token endpoint must accept authenticated peer sessions
      const tgR = await fetch(`${srv.base}/telegram/helper/token`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${peerToken}` },
        body: JSON.stringify({}),
      });
      // 401/403 = auth failure (bug); 503 = Telegram not configured (ok); 200 = ok
      if (tgR.status === 401 || tgR.status === 403) {
        throw new Error(`Peer session rejected on /telegram/helper/token: ${tgR.status}`);
      }
    });

    // ── PC-5: /session/recover returns {session_token, role, recovery_code} ─
    let recoveredToken, recoveredRole;
    await t.run('PC-5: /session/recover returns {session_token, role, recovery_code}', async () => {
      const initR = await fetch(`${srv.base}/session/init`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ role: 'client' }),
      });
      const { recovery_code } = await initR.json();

      const recoverR = await fetch(`${srv.base}/session/recover`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ recovery_code }),
      });
      if (recoverR.status !== 200) throw new Error(`/session/recover ${recoverR.status}`);
      const data = await recoverR.json();

      if (!data.session_token)  throw new Error('/session/recover missing session_token');
      if (!data.role)           throw new Error('/session/recover missing role');
      if (!data.recovery_code)  throw new Error('/session/recover missing new recovery_code');
      if (data.role !== 'client') throw new Error(`Expected role=client, got ${data.role}`);

      recoveredToken = data.session_token;
      recoveredRole  = data.role;

      // Recovered token must work for /session/status
      const statusR = await fetch(`${srv.base}/session/status`, {
        headers: { 'Authorization': `Bearer ${recoveredToken}` },
      });
      if (!statusR.ok) throw new Error(`Recovered token rejected by /session/status: ${statusR.status}`);
      const statusData = await statusR.json();
      if (statusData.role !== recoveredRole) {
        throw new Error(`Role mismatch in /session/status: expected ${recoveredRole}, got ${statusData.role}`);
      }
    });

    // ── PC-6: Token stored as naroom_session_{role} authorises /resume ──────
    await t.run('PC-6: Recovered token (stored as naroom_session_{role}) enables /resume', async () => {
      // Simulates: frontend stores token under naroom_session_client (for role=client)
      // then calls GET /resume — must NOT be 401
      const resumeR = await fetch(`${srv.base}/resume`, {
        headers: { 'Authorization': `Bearer ${recoveredToken}` },
      });
      if (resumeR.status === 401) {
        throw new Error(`Recovered token rejected by /resume (401) — token-to-role storage is broken`);
      }
      // 404 = no resources for this principal (expected); 200 = found something (also fine)
    });

    // ── PC-7: /listing/{id}/responses without auth → 401 ───────────────────
    await t.run('PC-7: GET /listing/{id}/responses without Authorization → 401', async () => {
      const r = await fetch(`${srv.base}/listing/${listingId}/responses`);
      if (r.status !== 401) throw new Error(`Expected 401, got ${r.status}`);
    });

    // ── PC-8: /chat/{id} without auth → 401 ────────────────────────────────
    await t.run('PC-8: GET /chat/{id} without Authorization → 401', async () => {
      const r = await fetch(`${srv.base}/chat/room_fake_pc8_test`);
      if (r.status !== 401) throw new Error(`Expected 401, got ${r.status}`);
    });

    // ── PC-9: /wallet/register response never carries session_token ─────────
    await t.run('PC-9: /wallet/register response does not include session_token', async () => {
      // Already covered in PC-2 but isolated here as an explicit contract assertion
      const initR = await fetch(`${srv.base}/session/init`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ role: 'client' }),
      });
      const { session_token: tok } = await initR.json();

      const regR = await fetch(`${srv.base}/wallet/register`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${tok}` },
        body: JSON.stringify({ wallet_address: WALLET, currency: 'BTC', role: 'client' }),
      });
      const regData = await regR.json();
      if (regData.session_token !== undefined) {
        throw new Error('/wallet/register must not return session_token in response body');
      }
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
