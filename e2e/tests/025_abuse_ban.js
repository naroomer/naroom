// 025_abuse_ban.js — abuse report ban thresholds (RP-4)
// 3 reports → banned_until = now+259200 (72h)
// 5 reports → banned_until = now+10years
// NOTE: ban enforcement (checking banned_until in listing/respond) is NOT IMPLEMENTED
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { assertStatus } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';
import { createHmac } from 'crypto';

const TEST_SALT = 'e2e-test-salt';

// Mirror of Go crypto.WalletHash used by the backend (must match server.js walletHash)
function walletHash(address) {
  const addr = address.trim();
  const lower = addr.toLowerCase();
  const normalized = (lower.startsWith('bc1') || lower.startsWith('ltc1')) ? lower : addr;
  return createHmac('sha256', Buffer.from(TEST_SALT))
    .update('naroom:v1:' + normalized)
    .digest('hex');
}

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_WALLETS  = [
  '1BoatSLRHtKNngkdXEeobR76b53LETtpyT',
  '1CounterpartyXXXXXXXXXXXXXXXUWLpVr',
  '1Bud1FAnonymousDonationsXXXbfMFMn',
  '1NiNja1bUmhSoTXozBRBEtR8LeF9TkDDmj',
  '1dice8EMZmqKvrGE4Qc9bUFf9PX3xaYDp',
];

export async function run() {
  console.log('\n=== 025: Abuse Report Ban Thresholds (RP-4) ===');
  console.log('  Note: ban enforcement (checking banned_until in listing/respond) is NOT IMPLEMENTED');
  const srv = new TestServer({ devMode: true });
  const t = new Runner('025_abuse_ban');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    // Register client + all 5 peers
    await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
    for (const pw of PEER_WALLETS) {
      await api.verifyWallet(pw, 'BTC', 'peer');
    }

    const clientHash = walletHash(CLIENT_WALLET);
    const now = Math.floor(Date.now() / 1000);

    // Inject 5 closed chat_rooms — one per peer — so each peer can legitimately report.
    // All statements run in a single sqlite3 invocation so PRAGMA foreign_keys = OFF applies.
    await t.run('inject 5 closed chat_rooms via DB', async () => {
      const stmts = ['PRAGMA foreign_keys = OFF'];
      for (let i = 0; i < PEER_WALLETS.length; i++) {
        const peerHash  = walletHash(PEER_WALLETS[i]);
        const roomId    = `room_rp4_${i}`;
        const listingId = `listing_rp4_${i}`;
        stmts.push(
          `INSERT OR IGNORE INTO chat_rooms ` +
          `(id, listing_id, client_hash, counselor_hash, client_pubkey, counselor_pubkey, status, started_at, closed_at, closed_by, expires_at) ` +
          `VALUES (` +
          `'${roomId}', '${listingId}', '${clientHash}', '${peerHash}', ` +
          `'cpubkey_${i}', 'ppubkey_${i}', 'closed', ${now - 3600}, ${now - 1800}, 'client', ${now + 3600}` +
          `)`
        );
      }
      stmts.push('PRAGMA foreign_keys = ON');
      srv.db(stmts.join('; '));
    });

    // Peers 0-2 report → after 3rd, banned_until ≈ now+259200
    for (let i = 0; i < 3; i++) {
      const idx = i;
      await t.run(`peer ${idx + 1} submits abuse report → 200`, async () => {
        const roomId = `room_rp4_${idx}`;
        const r = await api.abuseReport(roomId, ['misuse'], PEER_WALLETS[idx]);
        assertStatus(r, 200, `peer ${idx + 1} abuse report`);
      });
    }

    await t.run('after 3rd report: banned_until ≈ now + 259200 (72h)', async () => {
      const raw = srv.db(
        `SELECT banned_until FROM abuse_counters WHERE client_hash = '${clientHash}'`
      );
      const bannedUntil = parseInt(raw, 10);
      const expected = now + 259200;
      const diff = Math.abs(bannedUntil - expected);
      if (diff > 10) {
        throw new Error(
          `banned_until=${bannedUntil}, expected ≈${expected} (now+259200), diff=${diff}s`
        );
      }
    });

    // Peers 3-4 report → after 5th, banned_until ≈ now+10years
    for (let i = 3; i < 5; i++) {
      const idx = i;
      await t.run(`peer ${idx + 1} submits abuse report → 200`, async () => {
        const roomId = `room_rp4_${idx}`;
        const r = await api.abuseReport(roomId, ['misuse'], PEER_WALLETS[idx]);
        assertStatus(r, 200, `peer ${idx + 1} abuse report`);
      });
    }

    await t.run('after 5th report: banned_until ≈ now + 10 years', async () => {
      const raw = srv.db(
        `SELECT banned_until FROM abuse_counters WHERE client_hash = '${clientHash}'`
      );
      const bannedUntil = parseInt(raw, 10);
      const tenYears    = 10 * 365 * 24 * 3600;
      const expected    = now + tenYears;
      const diff        = Math.abs(bannedUntil - expected);
      if (diff > 60) {
        throw new Error(
          `banned_until=${bannedUntil}, expected ≈${expected} (now+10yr), diff=${diff}s`
        );
      }
    });

    await t.run('abuse_counters.total = 5', async () => {
      const total = parseInt(
        srv.db(`SELECT total FROM abuse_counters WHERE client_hash = '${clientHash}'`),
        10
      );
      if (total !== 5) throw new Error(`expected total=5, got ${total}`);
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
