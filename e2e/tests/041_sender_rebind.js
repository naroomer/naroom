// 041_sender_rebind.js — actual payment sender is the authority
//
// New model: typed wallet at registration is a preliminary check only.
// On-chain payment sender determines who owns/controls the listing or chat.
//
// Test B: Client pays listing invoice from different wallet B
//   B1: B passes client balance ($135+) → listing.wallet_hash = hash(B), listing active
//   B2: B fails client balance (<$135)  → listing NOT activated, owner unchanged
//
// Test A: Peer pays chat invoice from different wallet B
//   A1: B passes peer balance ($975+)   → chat_room.counselor_hash = hash(B), count=1
//   A2: B fails peer balance (<$975)    → chat NOT created, count stays 0, invoice rejected
//
// Server runs with DEV_SKIP_PAYMENTS=false + DEV_SEED_PRICES=true + chain stub
// so resolveSender() performs real balance checks without an external price API.

import { createHmac } from 'node:crypto';
import { TestServer, sleep } from '../lib/server.js';
import { newKeypair } from '../lib/crypto.js';
import { Runner } from '../lib/runner.js';
import { startChainStub } from '../lib/chain_stub.js';

const TEST_SALT = 'e2e-test-salt';
function walletHash(address) {
  const addr = address.trim();
  const lower = addr.toLowerCase();
  const normalized = (lower.startsWith('bc1') || lower.startsWith('ltc1')) ? lower : addr;
  return createHmac('sha256', Buffer.from(TEST_SALT))
    .update('naroom:v1:' + normalized)
    .digest('hex');
}

// Registered wallets (typed at registration)
const CLIENT_A = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_A   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

// Actual payer wallets (different from registered)
const WALLET_B       = '1EHNa6Q4Jz2uvNExL497mE43ikXhwF6kZm'; // passes balance
const WALLET_B_LOW   = '1FLAMEN6rq2BqMnkUmsJBqCTFd9sKKrWEp'; // insufficient balance

// At DEV_SEED_PRICES, BTC price = $100,000
// Thresholds: listing sender needs $135+ (sat=135_000), chat sender needs $975+ (sat=975_000)
const GENEROUS_SATS     = 50_000_000; // 0.5 BTC ≈ $50,000 — well above both thresholds
const LOW_CLIENT_SATS   = 134_999;    // $134.999 — 1 sat below $135 listing threshold
const LOW_PEER_SATS     = 974_999;    // $974.999 — 1 sat below $975 chat threshold

const LISTING_SATS   = 5000;           // $5 at $100k/BTC
const LISTING_CRYPTO = '0.00005000';
const CHAT_SATS      = 15000;          // $15 at $100k/BTC
const CHAT_CRYPTO    = '0.00015000';

export async function run() {
  console.log('\n=== 041: Sender Rebind (actual payer is the authority) ===');
  const t = new Runner('041_sender_rebind');

  const stub = await startChainStub();
  const srv = new TestServer({
    devMode: true,
    extraEnv: {
      DEV_MODE: 'false',
      DEV_SKIP_PAYMENTS: 'false',
      DEV_SEED_PRICES: 'true',          // $100k/BTC — no external price API needed
      MEMPOOL_API: stub.url + '/mempool',
      BLOCKCYPHER_API: stub.url + '/blockcypher',
      INVOICE_WATCH_INTERVAL: '1',      // 1s poll for fast tests
    },
  });

  try {
    await srv.start();

    // Register wallets directly — bypass balance check (stub has no initial state).
    srv.registerDirect(CLIENT_A, 'client', 'BTC');
    srv.registerDirect(PEER_A,   'peer',   'BTC', 1000);

    const hashA_client = walletHash(CLIENT_A);
    const hashA_peer   = walletHash(PEER_A);
    const hashB        = walletHash(WALLET_B);
    const hashB_low    = walletHash(WALLET_B_LOW);

    let seq = 0;
    const now = () => Math.floor(Date.now() / 1000);

    // ── Test B: Listing invoice paid from different wallet ─────────────────────

    await t.run('B1: listing paid by wallet B (sufficient balance) → wallet_hash rebound to hash(B)', async () => {
      const ts  = now();
      const id  = `041b1_${++seq}_${ts}`;
      const listingId   = `lst_${id}`;
      const invoiceId   = `inv_${id}`;
      const invoiceAddr = `mock_${id}`;

      // Listing starts with wallet_hash = hash(A)
      srv.db(`INSERT INTO listings (id, city, dependency_type, help_type, urgency, languages, wallet_hash, visible_until, created_at, status, is_sample, opened_chats_count)
              VALUES ('${listingId}', 'new_york', 'alcohol', 'crisis', 'urgent', '["en"]', '${hashA_client}', ${ts+3600}, ${ts}, 'pending', 0, 0)`);
      srv.db(`INSERT INTO invoices (id, type, address, amount_usd, amount_crypto, currency, listing_id, payer_address, price_at_creation, status, created_at)
              VALUES ('${invoiceId}', 'listing', '${invoiceAddr}', 5.0, '${LISTING_CRYPTO}', 'BTC', '${listingId}', '${hashA_client}', 100000.0, 'pending', ${ts})`);

      // Payment comes from WALLET_B — different from registered CLIENT_A
      await stub.setAddressState(invoiceAddr, {
        txs: [{ txid: `txB1_${id}`, value_sats: LISTING_SATS, confirmations: 2, senders: [WALLET_B] }],
        balance_sats: GENEROUS_SATS,
      });
      await stub.setAddressState(WALLET_B, { balance_sats: GENEROUS_SATS });

      // Wait for watcher to activate and rebind
      const deadline = Date.now() + 12000;
      let activated = false;
      while (Date.now() < deadline) {
        if (srv.db(`SELECT status FROM listings WHERE id='${listingId}'`) === 'active') {
          activated = true;
          break;
        }
        await sleep(1000);
      }
      if (!activated) throw new Error(`B1: listing not activated (status=${srv.db(`SELECT status FROM listings WHERE id='${listingId}'`)})`);

      // Owner must be rebound to hash(B)
      const owner = srv.db(`SELECT wallet_hash FROM listings WHERE id='${listingId}'`);
      if (owner !== hashB) {
        throw new Error(`B1: expected wallet_hash=hash(B), got ${owner} (hash(A)=${hashA_client})`);
      }
    });

    await t.run('B2: listing paid by wallet B_low (insufficient balance) → NOT activated, owner unchanged', async () => {
      const ts  = now();
      const id  = `041b2_${++seq}_${ts}`;
      const listingId   = `lst_${id}`;
      const invoiceId   = `inv_${id}`;
      const invoiceAddr = `mock_${id}`;

      srv.db(`INSERT INTO listings (id, city, dependency_type, help_type, urgency, languages, wallet_hash, visible_until, created_at, status, is_sample, opened_chats_count)
              VALUES ('${listingId}', 'new_york', 'alcohol', 'crisis', 'urgent', '["en"]', '${hashA_client}', ${ts+3600}, ${ts}, 'pending', 0, 0)`);
      srv.db(`INSERT INTO invoices (id, type, address, amount_usd, amount_crypto, currency, listing_id, payer_address, price_at_creation, status, created_at)
              VALUES ('${invoiceId}', 'listing', '${invoiceAddr}', 5.0, '${LISTING_CRYPTO}', 'BTC', '${listingId}', '${hashA_client}', 100000.0, 'pending', ${ts})`);

      // Payment from WALLET_B_LOW — 1 sat below the $135 listing threshold
      await stub.setAddressState(invoiceAddr, {
        txs: [{ txid: `txB2_${id}`, value_sats: LISTING_SATS, confirmations: 2, senders: [WALLET_B_LOW] }],
        balance_sats: LOW_CLIENT_SATS,
      });
      await stub.setAddressState(WALLET_B_LOW, { balance_sats: LOW_CLIENT_SATS });

      await sleep(5000); // multiple watcher cycles

      const status = srv.db(`SELECT status FROM listings WHERE id='${listingId}'`);
      if (status === 'active') {
        throw new Error(`B2: listing activated despite B_low having insufficient balance`);
      }
      const invStatus = srv.db(`SELECT status FROM invoices WHERE id='${invoiceId}'`);
      if (invStatus !== 'rejected') {
        throw new Error(`B2: expected invoice=rejected, got ${invStatus}`);
      }
      // Owner must NOT have changed
      const owner = srv.db(`SELECT wallet_hash FROM listings WHERE id='${listingId}'`);
      if (owner !== hashA_client) {
        throw new Error(`B2: wallet_hash changed despite rejection (got ${owner})`);
      }
    });

    // ── Test A: Chat invoice paid from different wallet ────────────────────────

    await t.run('A1: peer chat invoice paid by wallet B (sufficient balance) → room for hash(B), count=1', async () => {
      const ts  = now();
      const id  = `041a1_${++seq}_${ts}`;
      const listingId   = `lst_${id}`;
      const responseId  = `rsp_${id}`;
      const invoiceId   = `inv_${id}`;
      const invoiceAddr = `mock_${id}`;
      const peerKeys    = newKeypair();
      const clientKeys  = newKeypair();

      // Active listing owned by CLIENT_A
      srv.db(`INSERT INTO listings (id, city, dependency_type, help_type, urgency, languages, wallet_hash, visible_until, created_at, status, is_sample, opened_chats_count)
              VALUES ('${listingId}', 'new_york', 'alcohol', 'crisis', 'urgent', '["en"]', '${hashA_client}', ${ts+3600}, ${ts}, 'active', 0, 0)`);

      // Response from PEER_A — counselor_hash = hash(A_peer)
      srv.db(`INSERT INTO responses (id, listing_id, counselor_hash, counselor_pubkey, status, created_at)
              VALUES ('${responseId}', '${listingId}', '${hashA_peer}', '${peerKeys.pub}', 'accepted', ${ts})`);

      // Chat invoice: payer=hash(A_peer), but actual payment will come from WALLET_B
      srv.db(`INSERT INTO invoices (id, type, address, amount_usd, amount_crypto, currency, response_id, client_pubkey, payer_address, price_at_creation, status, created_at)
              VALUES ('${invoiceId}', 'chat', '${invoiceAddr}', 15.0, '${CHAT_CRYPTO}', 'BTC', '${responseId}', '${clientKeys.pub}', '${hashA_peer}', 100000.0, 'pending', ${ts})`);

      // Payment from WALLET_B — sufficient balance ($975+ threshold)
      await stub.setAddressState(invoiceAddr, {
        txs: [{ txid: `txA1_${id}`, value_sats: CHAT_SATS, confirmations: 2, senders: [WALLET_B] }],
        balance_sats: GENEROUS_SATS,
      });
      await stub.setAddressState(WALLET_B, { balance_sats: GENEROUS_SATS });

      // Wait for watcher to create chat room
      const deadline = Date.now() + 12000;
      let roomId = null;
      while (Date.now() < deadline) {
        roomId = srv.db(`SELECT id FROM chat_rooms WHERE listing_id='${listingId}' LIMIT 1`);
        if (roomId) break;
        await sleep(1000);
      }
      if (!roomId) throw new Error(`A1: chat room not created within 12s`);

      // Chat room must be for hash(B), NOT hash(A_peer)
      const counselor = srv.db(`SELECT counselor_hash FROM chat_rooms WHERE id='${roomId}'`);
      if (counselor !== hashB) {
        throw new Error(`A1: expected counselor_hash=hash(B)=${hashB}, got ${counselor} (hash(A_peer)=${hashA_peer})`);
      }

      // opened_chats_count must be 1
      const count = parseInt(srv.db(`SELECT COALESCE(opened_chats_count,0) FROM listings WHERE id='${listingId}'`), 10);
      if (count !== 1) throw new Error(`A1: expected opened_chats_count=1, got ${count}`);
    });

    await t.run('A2: peer chat invoice paid by wallet B_low (insufficient balance) → no room, count=0', async () => {
      const ts  = now();
      const id  = `041a2_${++seq}_${ts}`;
      const listingId   = `lst_${id}`;
      const responseId  = `rsp_${id}`;
      const invoiceId   = `inv_${id}`;
      const invoiceAddr = `mock_${id}`;
      const peerKeys    = newKeypair();
      const clientKeys  = newKeypair();

      srv.db(`INSERT INTO listings (id, city, dependency_type, help_type, urgency, languages, wallet_hash, visible_until, created_at, status, is_sample, opened_chats_count)
              VALUES ('${listingId}', 'new_york', 'alcohol', 'crisis', 'urgent', '["en"]', '${hashA_client}', ${ts+3600}, ${ts}, 'active', 0, 0)`);
      srv.db(`INSERT INTO responses (id, listing_id, counselor_hash, counselor_pubkey, status, created_at)
              VALUES ('${responseId}', '${listingId}', '${hashA_peer}', '${peerKeys.pub}', 'accepted', ${ts})`);
      srv.db(`INSERT INTO invoices (id, type, address, amount_usd, amount_crypto, currency, response_id, client_pubkey, payer_address, price_at_creation, status, created_at)
              VALUES ('${invoiceId}', 'chat', '${invoiceAddr}', 15.0, '${CHAT_CRYPTO}', 'BTC', '${responseId}', '${clientKeys.pub}', '${hashA_peer}', 100000.0, 'pending', ${ts})`);

      // Payment from WALLET_B_LOW — 1 sat below the $975 peer threshold
      await stub.setAddressState(invoiceAddr, {
        txs: [{ txid: `txA2_${id}`, value_sats: CHAT_SATS, confirmations: 2, senders: [WALLET_B_LOW] }],
        balance_sats: LOW_PEER_SATS,
      });
      await stub.setAddressState(WALLET_B_LOW, { balance_sats: LOW_PEER_SATS });

      await sleep(5000); // multiple watcher cycles

      // No chat room must exist
      const roomCount = parseInt(srv.db(`SELECT COUNT(*) FROM chat_rooms WHERE listing_id='${listingId}'`), 10);
      if (roomCount !== 0) {
        throw new Error(`A2: chat room created despite B_low having insufficient balance`);
      }
      // opened_chats_count must remain 0
      const count = parseInt(srv.db(`SELECT COALESCE(opened_chats_count,0) FROM listings WHERE id='${listingId}'`), 10);
      if (count !== 0) throw new Error(`A2: expected opened_chats_count=0 (no room), got ${count}`);

      // Invoice must be rejected
      const invStatus = srv.db(`SELECT status FROM invoices WHERE id='${invoiceId}'`);
      if (invStatus !== 'rejected') {
        throw new Error(`A2: expected invoice=rejected (low balance), got ${invStatus}`);
      }
    });

  } finally {
    await srv.stop();
    await stub.close();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
