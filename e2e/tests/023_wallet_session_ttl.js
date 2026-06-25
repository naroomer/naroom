// 023_wallet_session_ttl.js — wallet_sessions pruned when auth session expires (WK-3)
// ttl_cleaner.go deletes wallet_sessions WHERE wallet_hash NOT IN
// (SELECT wallet_hash FROM sessions WHERE expires_at > now AND revoked_at IS NULL)
import { TestServer, sleep } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';

export async function run() {
  console.log('\n=== 023: Wallet Session TTL (WK-3) ===');
  const srv = new TestServer({ devMode: true });
  const t = new Runner('023_wallet_session_ttl');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    await t.run('register client wallet → wallet_session created', async () => {
      const r = await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      if (r.status !== 200) throw new Error(`verifyWallet failed: ${r.status} ${JSON.stringify(r.body)}`);
    });

    await t.run('wallet_session exists in DB after registration', async () => {
      const count = parseInt(
        srv.db(`SELECT COUNT(*) FROM wallet_sessions WHERE role='client'`),
        10
      );
      if (count < 1) throw new Error(`expected >= 1 wallet_session for client, got ${count}`);
    });

    await t.run('force-expire all auth sessions for this wallet', async () => {
      // Set expires_at in the past so the cleaner considers them expired
      srv.db(`UPDATE sessions SET expires_at = strftime('%s','now') - 1`);
    });

    // TTL_CLEAN_INTERVAL=5 → cleaner runs every 5s; wait 7s to be safe
    await sleep(7000);

    await t.run('wallet_session is removed after auth session expires', async () => {
      const count = parseInt(
        srv.db(`SELECT COUNT(*) FROM wallet_sessions WHERE role='client'`),
        10
      );
      if (count !== 0) throw new Error(`expected 0 wallet_sessions after expiry, got ${count}`);
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
