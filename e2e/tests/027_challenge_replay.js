// 027_challenge_replay.js — wallet trust model (replaces old intentionally-failing challenge test)
//
// ARCHITECTURE NOTE:
//   NA Room does NOT use challenge-signature (Bitcoin message signing) for wallet ownership proof.
//   This is an intentional design decision — wallet control is proven at PAYMENT TIME by
//   on-chain sender address verification, not at registration time by cryptographic signature.
//
//   /wallet/register  = public balance pre-check only (NOT ownership proof)
//   Ownership proof   = payment sender hash match (invoice_watcher.go: verifySenderAndBalance)
//   Chat gate         = sender match + post-payment balance check — BOTH must pass
//
//   No /wallet/challenge endpoint exists or is planned.
//   The full payment-sender verification model is covered by 035_payment_verification.js.
//
// This test verifies three concrete behaviors of the trust model:
//   T1: /session/init + /wallet/register succeed — session obtained, wallet linked (balance pre-check only)
//   T2: A peer wallet can also register — proves no ownership assertion at register time
//   T3: A session obtained via /wallet/register (no payment) cannot open a chat room
//       (same invariant as IN-0; here tested purely via HTTP, without the chain stub machinery of 035)

import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { Runner } from '../lib/runner.js';
import { newKeypair } from '../lib/crypto.js';
import { createHmac } from 'node:crypto';

// devMode = true so /wallet/register bypasses the real blockchain balance API.
// This is the standard mode for most E2E tests.

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_WALLET   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

// Mirror of server walletHash to construct expected payer_address values
const TEST_HASH_KEY = 'e2e-test-salt';
function walletHash(address) {
  const addr = address.trim();
  const lower = addr.toLowerCase();
  const normalized = (lower.startsWith('bc1') || lower.startsWith('ltc1')) ? lower : addr;
  return createHmac('sha256', Buffer.from(TEST_HASH_KEY))
    .update('naroom:v1:' + normalized)
    .digest('hex');
}

export async function run() {
  console.log('\n=== 027: Wallet Trust Model (register=pre-check; ownership proven at payment) ===');
  const srv = new TestServer({ devMode: true });
  const t = new Runner('027_challenge_replay');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    // ── T1: /session/init + /wallet/register flow (balance pre-check only) ──
    await t.run('T1: /session/init returns session_token; /wallet/register links wallet (no signature)', async () => {
      const r = await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      if (r.status !== 200) {
        throw new Error(`Expected 200, got ${r.status}: ${JSON.stringify(r.body)}`);
      }
      if (!r.body.wallet_linked) {
        throw new Error(`Expected wallet_linked: true, got: ${JSON.stringify(r.body)}`);
      }
      // Confirm: no challenge or signature field — registration is purely balance-based
      if (r.body.challenge || r.body.nonce || r.body.sign_message) {
        throw new Error(
          `FAIL: register response contains unexpected challenge field — ` +
          `architecture does not use challenge-signature: ${JSON.stringify(r.body)}`
        );
      }
      // session_token is obtained from /session/init, NOT /wallet/register
      if (r.body.session_token) {
        throw new Error(`/wallet/register must NOT return session_token — use /session/init instead`);
      }
    });

    // ── T2: peer /wallet/register — proves ownership is not asserted ──
    await t.run('T2: peer /session/init + /wallet/register succeeds; no ownership assertion, no signature required', async () => {
      const r = await api.verifyWallet(PEER_WALLET, 'BTC', 'peer');
      if (r.status !== 200) {
        throw new Error(`Expected 200, got ${r.status}: ${JSON.stringify(r.body)}`);
      }
      if (!r.body.wallet_linked) {
        throw new Error(`Expected wallet_linked: true, got: ${JSON.stringify(r.body)}`);
      }
      // The trust model note: balance is pre-checked at registration time,
      // but wallet control is proven at payment time (on-chain sender verification).
    });

    // ── T3: Session from /wallet/register (no payment) cannot open a chat room ──
    // This is the same IN-0 invariant tested by 035 T1, but verified here via a simpler
    // HTTP-only path: a peer with a valid session but no payment cannot GET /peer/chatroom.
    await t.run('T3: registered-only peer (no payment) → GET /peer/chatroom returns no active room', async () => {
      const peerKeys   = newKeypair();
      const clientKeys = newKeypair();
      const ts = Math.floor(Date.now() / 1000);
      const listingId = 'lst-027-t3-' + ts;

      // Insert active listing directly — simulates a client who already paid to activate their listing
      srv.db(
        `INSERT INTO listings (id, city, dependency_type, help_type, urgency, languages, ` +
        `wallet_hash, visible_until, created_at, status) VALUES ` +
        `('${listingId}', 'new_york', 'alcohol', 'crisis', 'urgent', 'en', ` +
        `'${walletHash(CLIENT_WALLET)}', ${ts + 3600}, ${ts}, 'active')`
      );

      // Peer responds to the listing using their registered session
      const r1 = await api.respond(listingId, PEER_WALLET, peerKeys.pub);
      if (r1.status !== 201) {
        throw new Error(`Peer respond failed: ${r1.status} ${JSON.stringify(r1.body)}`);
      }

      // Peer tries to GET /peer/chatroom — no payment has been made, so no chat room exists
      const r2 = await api.getPeerChatroom(PEER_WALLET, listingId);
      if (r2.status === 200 && r2.body.room_id) {
        throw new Error(
          `T3 FAIL: peer sees a chat room without any payment. ` +
          `This violates IN-0: register-only must NOT open chat. body=${JSON.stringify(r2.body)}`
        );
      }
      // 404 or 400 — no room exists; invariant holds
    });

    // ── T4: /wallet/challenge returns 404 (endpoint intentionally absent) ──
    // Document the architecture decision: no challenge endpoint exists or is planned.
    await t.run('T4: /wallet/challenge returns 404 — no challenge-signature in architecture (by design)', async () => {
      const r = await fetch(`${srv.base}/wallet/challenge`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ address: CLIENT_WALLET }),
      });
      if (r.status !== 404) {
        throw new Error(
          `Expected 404 (endpoint absent by design), got ${r.status}. ` +
          `If /wallet/challenge now exists, remove this test and update architecture docs.`
        );
      }
      // 404 = correct — ownership proof happens at payment time, not registration time
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
