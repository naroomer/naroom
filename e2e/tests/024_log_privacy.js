// 024_log_privacy.js — server logs must not contain raw IP, wallet address, or session token (ID-5)
import { TestServer, sleep } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';

export async function run() {
  console.log('\n=== 024: Log Privacy (ID-5) ===');
  const srv = new TestServer({ devMode: true });
  const t = new Runner('024_log_privacy');

  try {
    await srv.start();

    // Start collecting all stderr output from the backend process
    const logLines = [];
    srv.proc.stderr.on('data', d => {
      for (const l of d.toString().split('\n')) {
        if (l.trim()) logLines.push(l);
      }
    });
    srv.proc.stdout.on('data', d => {
      for (const l of d.toString().split('\n')) {
        if (l.trim()) logLines.push(l);
      }
    });

    const api = new ApiClient(srv.base);

    await t.run('register wallet (generates log traffic)', async () => {
      const r = await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      if (r.status !== 200) throw new Error(`verifyWallet failed: ${r.status}`);
    });

    await t.run('create listing (more log traffic)', async () => {
      const r = await api.createListing(CLIENT_WALLET);
      if (r.status !== 201) throw new Error(`createListing failed: ${r.status}`);
    });

    await t.run('GET /board/new_york (more log traffic)', async () => {
      const r = await api.getBoard('new_york');
      if (r.status !== 200) throw new Error(`getBoard failed: ${r.status}`);
    });

    // Allow log lines to flush
    await sleep(500);

    const sessionToken = api.getToken(CLIENT_WALLET);

    await t.run('logs do not contain raw wallet address', async () => {
      const leaking = logLines.filter(l => l.includes(CLIENT_WALLET));
      if (leaking.length > 0) {
        throw new Error(
          `Found ${leaking.length} log line(s) containing wallet address:\n  ${leaking.slice(0, 3).join('\n  ')}`
        );
      }
    });

    await t.run('logs do not contain raw loopback IP 127.0.0.1', async () => {
      const leaking = logLines.filter(l => l.includes('127.0.0.1'));
      if (leaking.length > 0) {
        throw new Error(
          `Found ${leaking.length} log line(s) containing raw IP:\n  ${leaking.slice(0, 3).join('\n  ')}`
        );
      }
    });

    await t.run('logs do not contain session token', async () => {
      if (!sessionToken) throw new Error('session token is empty — verifyWallet may have failed');
      const leaking = logLines.filter(l => l.includes(sessionToken));
      if (leaking.length > 0) {
        throw new Error(
          `Found ${leaking.length} log line(s) containing session token:\n  ${leaking.slice(0, 3).join('\n  ')}`
        );
      }
    });

    await t.run('logs do not contain 64-char hex session token hash pattern', async () => {
      // Session token hashes stored in DB are 64-char hex; if the raw token (base64url ~43 chars)
      // or any 64-char hex appears in logs that matches a known secret, that is a leak.
      // We check that no log line contains a 64-char lowercase hex string at all.
      const hexPattern = /\b[0-9a-f]{64}\b/;
      const leaking = logLines.filter(l => hexPattern.test(l));
      if (leaking.length > 0) {
        throw new Error(
          `Found ${leaking.length} log line(s) containing 64-char hex string (possible token hash):\n  ${leaking.slice(0, 3).join('\n  ')}`
        );
      }
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
