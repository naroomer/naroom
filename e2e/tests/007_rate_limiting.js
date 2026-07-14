// 007_rate_limiting.js — rate limiting returns 429 after burst exceeded
import { TestServer } from '../lib/server.js';
import { sleep } from '../lib/server.js';
import { Runner } from '../lib/runner.js';

export async function run() {
  console.log('\n=== 007: Rate Limiting ===');
  const srv = new TestServer({ devMode: false });
  const t = new Runner('007_rate_limiting');

  try {
    await srv.start();

    // POST /session/init: burst=10, rate=10/min (same rlWalletVerify limiter)
    // /session/init doesn't require auth, so requests return 201 until burst is exhausted.
    // This tests the wallet verification rate limiter without needing a prior session.
    const initBody = JSON.stringify({ role: 'client' });
    await t.run('session/init: first 10 requests succeed (not 429)', async () => {
      for (let i = 0; i < 10; i++) {
        const r = await fetch(`${srv.base}/session/init`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: initBody,
        });
        if (r.status === 429) throw new Error(`Request ${i+1} got 429, expected 201 (rate limit not yet hit)`);
        if (r.status !== 201) throw new Error(`Request ${i+1} got ${r.status}, expected 201`);
      }
    });

    await t.run('session/init: 11th request → 429 (burst exhausted)', async () => {
      const r = await fetch(`${srv.base}/session/init`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: initBody,
      });
      if (r.status !== 429) throw new Error(`Expected 429, got ${r.status}`);
    });

    // /board is lenient (60/min burst 60) — just verify it doesn't rate limit immediately
    await t.run('board: 10 rapid requests all succeed (high burst limit)', async () => {
      for (let i = 0; i < 10; i++) {
        const r = await fetch(`${srv.base}/board/new_york`);
        if (r.status === 429) throw new Error(`Board request ${i+1} got 429 unexpectedly`);
      }
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
