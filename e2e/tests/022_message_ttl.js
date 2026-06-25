// 022_message_ttl.js — encrypted messages deleted after 24h by TTL cleaner (WK-1)
import { TestServer, sleep } from '../lib/server.js';
import { Runner } from '../lib/runner.js';

export async function run() {
  console.log('\n=== 022: Message TTL 24h (WK-1) ===');
  const srv = new TestServer({ devMode: true });
  const t = new Runner('022_message_ttl');

  try {
    await srv.start();

    const now = Math.floor(Date.now() / 1000);
    const oldTs = now - 86401; // 25 hours ago — past the 24h cutoff
    const freshTs = now;       // just now — should survive

    // All injections in one sqlite3 call so PRAGMA foreign_keys = OFF applies.
    await t.run('inject old message (25h ago) and fresh control message', async () => {
      srv.db(
        `PRAGMA foreign_keys = OFF; ` +
        `INSERT INTO encrypted_messages (id, room_id, sender_pubkey, nonce, ciphertext, msg_type, created_at) ` +
        `VALUES ('msg_wk1_old', 'fake-room-wk1', 'pubkey_old', 'nonce_old', 'ct_old', 'text', ${oldTs}); ` +
        `INSERT INTO encrypted_messages (id, room_id, sender_pubkey, nonce, ciphertext, msg_type, created_at) ` +
        `VALUES ('msg_wk1_fresh', 'fake-room-wk1', 'pubkey_fresh', 'nonce_fresh', 'ct_fresh', 'text', ${freshTs}); ` +
        `PRAGMA foreign_keys = ON`
      );
    });

    await t.run('both messages exist before TTL cleaner runs', async () => {
      const old   = parseInt(srv.db(`SELECT COUNT(*) FROM encrypted_messages WHERE id='msg_wk1_old'`), 10);
      const fresh = parseInt(srv.db(`SELECT COUNT(*) FROM encrypted_messages WHERE id='msg_wk1_fresh'`), 10);
      if (old !== 1)   throw new Error(`expected 1 old message, got ${old}`);
      if (fresh !== 1) throw new Error(`expected 1 fresh message, got ${fresh}`);
    });

    // TTL_CLEAN_INTERVAL=5 → cleaner runs every 5s; wait 7s to be safe
    await sleep(7000);

    await t.run('old message (25h) is deleted by TTL cleaner', async () => {
      const count = parseInt(srv.db(`SELECT COUNT(*) FROM encrypted_messages WHERE id='msg_wk1_old'`), 10);
      if (count !== 0) throw new Error(`expected 0 (deleted), got ${count}`);
    });

    await t.run('fresh message (0h) still exists after TTL cleaner', async () => {
      const count = parseInt(srv.db(`SELECT COUNT(*) FROM encrypted_messages WHERE id='msg_wk1_fresh'`), 10);
      if (count !== 1) throw new Error(`expected 1 (still alive), got ${count}`);
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
