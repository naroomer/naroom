// 028_payment_edge_cases
// Требует chain_stub (в e2e/lib/) + бэкенд с настраиваемыми URL API.
// DEV_SKIP_PAYMENTS не установлен — invoice watcher работает по-настоящему,
// но ходит в заглушку вместо реальных API.
//
// Сценарии:
//   a) недоплата: tx < суммы инвойса → инвойс НЕ подтверждён
//   b) две транзакции в сумме = инвойс → фиксируем политику (одна TX или сумма)
//   c) API недоступен (таймаут) → watcher не падает, ретраит, после восстановления — подтверждает
import { createHmac } from 'node:crypto';
import { TestServer, sleep } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair } from '../lib/crypto.js';
import { assertStatus, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';
import { startChainStub } from '../lib/chain_stub.js';

// Mirror of server.js walletHash() and Go's crypto.WalletHash()
// Uses the same TEST_SALT='e2e-test-salt' hardcoded in e2e/lib/server.js
const TEST_SALT = 'e2e-test-salt';
function walletHash(address) {
  const addr = address.trim();
  const lower = addr.toLowerCase();
  const normalized = (lower.startsWith('bc1') || lower.startsWith('ltc1')) ? lower : addr;
  return createHmac('sha256', Buffer.from(TEST_SALT))
    .update('naroom:v1:' + normalized)
    .digest('hex');
}

// Separate wallets per scenario — one active listing per wallet at a time
const WALLET_A = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const WALLET_B = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';
const WALLET_C = '1CounterpartyXXXXXXXXXXXXXXXUWLpVr';

export async function run() {
  console.log('\n=== 028: Payment Edge Cases ===');
  const t = new Runner('028_payment_edge_cases');

  const stub = await startChainStub();

  const srv = new TestServer({
    devMode: true, // still use dev build tag
    extraEnv: {
      DEV_MODE: 'false',           // turn off auto-confirm
      DEV_SKIP_PAYMENTS: 'false',  // invoice watcher must check real (stub) API
      DEV_SEED_PRICES: 'true',     // seed $100k BTC price so watcher balance check never needs external price API
      MEMPOOL_API: stub.url + '/mempool',
      BLOCKCYPHER_API: stub.url + '/blockcypher',
      INVOICE_WATCH_INTERVAL: '1', // poll every 1s for fast tests
    },
  });

  try {
    await srv.start();

    // Register all wallets directly (balance check would fail without real API)
    const api = new ApiClient(srv.base);
    for (const [wallet, idx] of [[WALLET_A, 0], [WALLET_B, 1], [WALLET_C, 2]]) {
      const token = srv.registerDirect(wallet, 'client', 'BTC');
      api.tokens[wallet] = { token, role: 'client' };
    }

    // Per-scenario wallets so "already have active listing" never triggers
    const scenarioWallets = [WALLET_A, WALLET_B, WALLET_C];
    let scenarioIndex = 0;

    // Helper: inject listing + invoice directly into the DB.
    // Avoids /listing/create which calls an external price API (unavailable in test env).
    // Uses $5 listing invoice at $100k/BTC → 5000 sats.
    // payer_address is queried from wallet_sessions (set by registerDirect).
    function createInvoice() {
      const wallet = scenarioWallets[scenarioIndex++];
      const ts = Math.floor(Date.now() / 1000);
      const idx = scenarioIndex; // 1-based after increment
      const listingId  = `lst_028_${idx}_${ts}`;
      const invoiceId  = `inv_028_${idx}_${ts}`;
      // Deterministic address — stub accepts any string as address key
      const invoiceAddress = `mock_btc_028_${idx}_${ts}`;
      const amountSats = 5000; // $5 at $100k/BTC
      const amountCrypto = '0.00005000';
      // payer_address = HMAC hash of the sender wallet address (same as wallet_sessions.wallet_hash)
      const payerAddress = walletHash(wallet);

      srv.db(
        `INSERT INTO listings (id, city, dependency_type, help_type, urgency, languages, wallet_hash, visible_until, created_at, status, is_sample) ` +
        `VALUES ('${listingId}', 'new_york', 'alcohol', 'crisis', 'urgent', '["en"]', '${payerAddress}', ${ts + 3600}, ${ts}, 'pending', 0)`
      );
      srv.db(
        `INSERT INTO invoices (id, type, address, amount_usd, amount_crypto, currency, listing_id, payer_address, price_at_creation, status, created_at) ` +
        `VALUES ('${invoiceId}', 'listing', '${invoiceAddress}', 5.0, '${amountCrypto}', 'BTC', '${listingId}', '${payerAddress}', 100000.0, 'pending', ${ts})`
      );

      return {
        listingId,
        invoiceId,
        invoiceAddress,
        amountSats,
        senderWallet: wallet, // raw address — stub keys by this; watcher hashes it to verify payer
      };
    }

    // Helper: get listing status
    async function listingStatus(listingId) {
      const r = await api.getListing(listingId);
      return r.body.status;
    }

    // ── (a) недоплата ────────────────────────────────────────────────────────
    await t.run('(a) underpayment: tx at 60% of invoice → NOT confirmed', async () => {
      const inv = createInvoice();
      const underpay = Math.floor(inv.amountSats * 0.6);

      await stub.setAddressState(inv.invoiceAddress, {
        txs: [{ txid: 'tx-under-' + inv.invoiceId, value_sats: underpay, confirmations: 2, senders: [inv.senderWallet] }],
        balance_sats: 50_000_000, // sender has plenty of BTC (> $150 threshold)
      });
      // Also register sender balance so payer verification passes the balance check
      await stub.setAddressState(inv.senderWallet, { balance_sats: 50_000_000 });

      // Give watcher 4 cycles to check (4s at interval=1)
      await sleep(4000);
      const status = await listingStatus(inv.listingId);
      if (status === 'active') {
        throw new Error(`UNDERPAYMENT: listing confirmed after paying only ${underpay} sats (${(underpay / inv.amountSats * 100).toFixed(0)}%)`);
      }
    });

    // ── (b) две транзакции ───────────────────────────────────────────────────
    await t.run('(b) two txs summing to invoice amount — record actual policy', async () => {
      const inv = createInvoice();
      const half = Math.ceil(inv.amountSats / 2);

      await stub.setAddressState(inv.invoiceAddress, {
        txs: [
          { txid: 'tx-p1-' + inv.invoiceId, value_sats: half, confirmations: 2, senders: [inv.senderWallet] },
          { txid: 'tx-p2-' + inv.invoiceId, value_sats: inv.amountSats - half, confirmations: 2, senders: [inv.senderWallet] },
        ],
        balance_sats: 50_000_000,
      });
      await stub.setAddressState(inv.senderWallet, { balance_sats: 50_000_000 });

      // Wait up to 10s for watcher to process
      let finalStatus = 'pending';
      const deadline = Date.now() + 10000;
      while (Date.now() < deadline) {
        finalStatus = await listingStatus(inv.listingId);
        if (finalStatus !== 'pending') break;
        await sleep(1000);
      }

      // Policy documentation (not a hard fail — both outcomes are valid):
      if (finalStatus === 'active') {
        console.log('  [info] policy: multi-TX summation IS supported — listing activated');
      } else {
        console.log(`  [info] policy: multi-TX summation NOT supported — status=${finalStatus} (requires single TX)`);
      }
      // Either way, must not be a 5xx-derived crash
    });

    // ── (c) API таймаут → сервер живёт, потом восстанавливается ─────────────
    await t.run('(c) API timeout: server stays alive, confirms after recovery', async () => {
      const inv = createInvoice();

      // Stage 1: stub times out
      await stub.setMode('timeout');
      await sleep(4000);

      // Server must still respond to health check
      const health = await fetch(srv.base + '/health');
      if (!health.ok) {
        throw new Error(`Server died while blockchain API was timing out (status ${health.status})`);
      }

      // Invoice must still be pending (not confirmed from nowhere)
      const midStatus = await listingStatus(inv.listingId);
      if (midStatus === 'active') {
        throw new Error('Invoice confirmed while API was unavailable — where did the data come from?');
      }

      // Stage 2: stub recovers and tx arrives
      await stub.setMode('ok');
      await stub.setAddressState(inv.invoiceAddress, {
        txs: [{ txid: 'tx-late-' + inv.invoiceId, value_sats: inv.amountSats, confirmations: 2, senders: [inv.senderWallet] }],
        balance_sats: 50_000_000,
      });
      await stub.setAddressState(inv.senderWallet, { balance_sats: 50_000_000 });

      await pollUntil(async () => {
        const s = await listingStatus(inv.listingId);
        return s === 'active' ? true : null;
      }, { timeout: 40000, label: 'listing confirmed after API recovery' });
    });

  } finally {
    await srv.stop();
    await stub.close();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
