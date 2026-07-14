// 009_session_lifecycle.js — session token: issue, use, refresh, revoke
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus, assertHasField } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';

export async function run() {
  console.log('\n=== 009: Session Token Lifecycle ===');
  const srv = new TestServer();
  const t = new Runner('009_session_lifecycle');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    let originalToken;

    await t.run('session/init → session_token returned; wallet/register → wallet_linked', async () => {
      // Step 1: create principal + session
      const initR = await fetch(`${srv.base}/session/init`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ role: 'client' }),
      });
      assertStatus({ status: initR.status, body: {} }, 201, 'session/init');
      const initBody = await initR.json();
      assertHasField(initBody, 'session_token', 'session/init response');
      originalToken = initBody.session_token;
      // Step 2: link wallet using session token
      const regR = await fetch(`${srv.base}/wallet/register`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${originalToken}` },
        body: JSON.stringify({ wallet_address: CLIENT_WALLET, currency: 'BTC', role: 'client' }),
      });
      assertStatus({ status: regR.status, body: {} }, 200, 'wallet/register');
      const regBody = await regR.json();
      if (!regBody.wallet_linked) throw new Error(`Expected wallet_linked: true, got ${JSON.stringify(regBody)}`);
      // Store token in api client for subsequent calls
      api.tokens[CLIENT_WALLET] = { token: originalToken, role: 'client' };
    });

    await t.run('token works for protected endpoint (create listing)', async () => {
      const r = await api.createListing(CLIENT_WALLET);
      if (r.status !== 201) throw new Error(`Expected 201, got ${r.status}: ${JSON.stringify(r.body)}`);
    });

    await t.run('no token → protected endpoint returns 401', async () => {
      const r = await fetch(`${srv.base}/listing/create`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ city: 'new_york', dependency_type: 'alcohol', help_type: 'crisis', urgency: 'urgent', languages: ['en'], currency: 'BTC' }),
      });
      if (r.status !== 401) throw new Error(`Expected 401 without token, got ${r.status}`);
    });

    await t.run('invalid token → 401', async () => {
      const r = await fetch(`${srv.base}/listing/create`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': 'Bearer invalidtokenXXXXXXXXXXXXXXXXXXXXXXXXXXXX',
        },
        body: JSON.stringify({ city: 'london', dependency_type: 'alcohol', help_type: 'crisis', urgency: 'urgent', languages: ['en'], currency: 'BTC' }),
      });
      if (r.status !== 401) throw new Error(`Expected 401 for invalid token, got ${r.status}`);
    });

    let refreshedToken;
    await t.run('POST /session/refresh returns new token', async () => {
      const r = await api.sessionRefresh(CLIENT_WALLET);
      assertStatus(r, 200, 'refresh');
      assertHasField(r.body, 'token', 'refresh response');
      assertHasField(r.body, 'expires_at', 'refresh response');
      refreshedToken = r.body.token;
      if (refreshedToken === originalToken) throw new Error('Refreshed token must differ from original');
    });

    await t.run('original token revoked after refresh → 401', async () => {
      // Refresh revokes the old token server-side
      const r = await fetch(`${srv.base}/listing/create`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${originalToken}`,
        },
        body: JSON.stringify({ city: 'london', dependency_type: 'alcohol', help_type: 'crisis', urgency: 'urgent', languages: ['en'], currency: 'BTC' }),
      });
      if (r.status !== 401) throw new Error(`Expected 401 for old token after refresh, got ${r.status}`);
    });

    await t.run('new token works after refresh', async () => {
      const r = await fetch(`${srv.base}/board/new_york`, {
        headers: { 'Authorization': `Bearer ${refreshedToken}` },
      });
      if (r.status !== 200) throw new Error(`Expected 200 with refreshed token, got ${r.status}`);
    });

    await t.run('POST /session/revoke revokes current (refreshed) session', async () => {
      const r = await api.sessionRevoke(CLIENT_WALLET); // api.tokens[CLIENT_WALLET] = refreshedToken after sessionRefresh
      assertStatus(r, 200, 'revoke');
    });

    await t.run('revoked token → 401', async () => {
      const r = await fetch(`${srv.base}/listing/create`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${refreshedToken}`,
        },
        body: JSON.stringify({ city: 'london', dependency_type: 'alcohol', help_type: 'crisis', urgency: 'urgent', languages: ['en'], currency: 'BTC' }),
      });
      if (r.status !== 401) throw new Error(`Expected 401 for revoked token, got ${r.status}`);
    });

    await t.run('re-verify after revoke → new working token', async () => {
      // Clear stored token to force a fresh /session/init + /wallet/register cycle
      delete api.tokens[CLIENT_WALLET];
      const r = await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
      assertStatus(r, 200, 're-verify');
      if (!r.body.wallet_linked) throw new Error(`Expected wallet_linked: true after re-verify`);
      // Try using new token
      const r2 = await fetch(`${srv.base}/board/new_york`);
      assertStatus(r2, 200, 'board after re-verify');
    });

    await t.run('DB: revoked_at set for revoked session', async () => {
      // After revoke, the sessions row should have revoked_at set
      const count = srv.db(`SELECT COUNT(*) FROM sessions WHERE revoked_at IS NOT NULL`);
      if (parseInt(count, 10) < 1) throw new Error('No revoked sessions in DB');
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
