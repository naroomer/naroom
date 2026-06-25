// 013_invoice_scoping.js — invoice status requires session and ownership
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { assertStatus, assertHasField, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_WALLET   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

export async function run() {
  console.log('\n=== 013: Invoice Scoping ===');
  const srv = new TestServer();
  const t = new Runner('013_invoice_scoping');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client');
    await api.verifyWallet(PEER_WALLET, 'BTC', 'peer');

    // Client creates listing → gets invoice_id
    let invoiceId;
    await t.run('client creates listing and gets invoice_id', async () => {
      const r = await api.createListing(CLIENT_WALLET);
      assertStatus(r, 201, 'create listing');
      assertHasField(r.body, 'invoice_id', 'create listing');
      invoiceId = r.body.invoice_id;
    });

    await t.run('GET /invoice/{id}/status without session → 401', async () => {
      const r = await fetch(`${srv.base}/invoice/${invoiceId}/status`);
      if (r.status !== 401) throw new Error(`Expected 401 without session, got ${r.status}`);
    });

    await t.run('invoice owner (client) can poll status', async () => {
      const r = await api.invoiceStatus(invoiceId, CLIENT_WALLET);
      assertStatus(r, 200, 'invoice status as owner');
      assertHasField(r.body, 'status', 'invoice response');
      assertHasField(r.body, 'address', 'invoice response');
      assertHasField(r.body, 'amount_crypto', 'invoice response');
    });

    await t.run('non-owner (peer) cannot see client invoice → 403', async () => {
      const r = await api.invoiceStatus(invoiceId, PEER_WALLET);
      assertStatus(r, 403, 'non-owner invoice access');
    });

    await t.run('invoice status shows a payment address', async () => {
      const r = await api.invoiceStatus(invoiceId, CLIENT_WALLET);
      // In dev mode address is a placeholder (btc_dev_N); in prod it's a real BTC address.
      // Just verify it's non-empty and present.
      const addr = r.body.address;
      if (!addr || addr.length < 1) throw new Error(`address missing or empty: ${addr}`);
    });

    await t.run('non-existent invoice → 404', async () => {
      const r = await api.invoiceStatus('inv_nonexistent_123', CLIENT_WALLET);
      if (r.status !== 404) throw new Error(`Expected 404, got ${r.status}`);
    });

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
