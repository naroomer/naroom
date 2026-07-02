// e2e/lib/chain_stub.js
// HTTP-заглушка, имитирующая mempool.space и BlockCypher.
// Управляется через control-эндпоинт: тест задаёт сценарий, watcher бэкенда
// ходит в заглушку как в реальный API.
//
// Использование в тесте:
//   const stub = await startChainStub();
//   const server = new TestServer({ extraEnv: {
//     MEMPOOL_API: stub.url + '/mempool',
//     BLOCKCYPHER_API: stub.url + '/blockcypher',
//   }});
//   stub.setAddressState('bc1q...', {
//     txs: [{ txid: 'aa', value_sats: 40000, confirmations: 1, senders: ['1Sender...'] }],
//     balance_sats: 500000,  // for balance check after payment
//   });
//
// tx fields:
//   txid          string
//   value_sats    number   — amount received at the invoice address
//   confirmations number   — 0 = unconfirmed, ≥1 = confirmed
//   senders       string[] — optional: sender addresses (vin). Required for payer verification.

import http from 'node:http';

export async function startChainStub() {
  // state: address → { txs, balance_sats }
  const state = new Map();
  let globalMode = 'ok'; // 'ok' | 'timeout' | 'error429' | 'error500'

  const server = http.createServer(async (req, res) => {
    const url = new URL(req.url, 'http://x');

    // ── control API (used only by tests) ────────────────────────────────────
    if (url.pathname === '/_control/set' && req.method === 'POST') {
      let body = '';
      for await (const chunk of req) body += chunk;
      const { address, txs, balance_sats, mode } = JSON.parse(body);
      if (address !== undefined) {
        state.set(address, { txs: txs ?? [], balance_sats: balance_sats ?? 0 });
      }
      if (mode !== undefined) globalMode = mode;
      res.writeHead(200).end('{"ok":true}');
      return;
    }

    // ── failure modes ────────────────────────────────────────────────────────
    if (globalMode === 'timeout') {
      // Hold connection longer than the Go HTTP client timeout (15s)
      const t = setTimeout(() => { try { res.writeHead(504).end(); } catch {} }, 30_000);
      req.on('close', () => clearTimeout(t));
      return;
    }
    if (globalMode === 'error429') {
      res.writeHead(429, { 'content-type': 'application/json' }).end('{"error":"rate limit"}');
      return;
    }
    if (globalMode === 'error500') {
      res.writeHead(500).end('{"error":"internal"}');
      return;
    }

    // ── mempool.space: GET /mempool/address/:addr/txs ────────────────────────
    let m = url.pathname.match(/^\/mempool\/address\/([^/]+)\/txs$/);
    if (m) {
      const addr = m[1];
      const s = state.get(addr) ?? { txs: [] };
      const txs = s.txs.map(t => ({
        txid: t.txid,
        status: {
          confirmed: t.confirmations > 0,
          block_height: t.confirmations > 0 ? 900000 : null,
        },
        vout: [{ scriptpubkey_address: addr, value: t.value_sats }],
        // vin: sender addresses for payer verification
        vin: (t.senders ?? []).map(senderAddr => ({
          prevout: { scriptpubkey_address: senderAddr },
        })),
      }));
      res.writeHead(200, { 'content-type': 'application/json' }).end(JSON.stringify(txs));
      return;
    }

    // ── mempool.space: GET /mempool/address/:addr (balance) ─────────────────
    m = url.pathname.match(/^\/mempool\/address\/([^/]+)$/);
    if (m) {
      const s = state.get(m[1]) ?? { balance_sats: 0 };
      const bal = s.balance_sats ?? 0;
      res.writeHead(200, { 'content-type': 'application/json' }).end(JSON.stringify({
        chain_stats: { funded_txo_sum: bal, spent_txo_sum: 0 },
        mempool_stats: { funded_txo_sum: 0, spent_txo_sum: 0 },
      }));
      return;
    }

    // ── BlockCypher: GET /blockcypher/addrs/:addr ────────────────────────────
    m = url.pathname.match(/^\/blockcypher\/addrs\/([^/]+)/);
    if (m) {
      const addr = m[1];
      const s = state.get(addr) ?? { txs: [], balance_sats: 0 };
      const txrefs = s.txs.map(t => ({
        tx_hash: t.txid,
        value: t.value_sats,
        confirmations: t.confirmations,
        addresses: t.senders ?? [],
      }));
      res.writeHead(200, { 'content-type': 'application/json' }).end(JSON.stringify({
        address: addr,
        balance: s.balance_sats ?? 0,
        txrefs,
      }));
      return;
    }

    res.writeHead(404).end();
  });

  await new Promise(r => server.listen(0, '127.0.0.1', r));
  const port = server.address().port;
  const stubUrl = `http://127.0.0.1:${port}`;

  return {
    url: stubUrl,
    async setAddressState(address, { txs = [], balance_sats = 0 } = {}) {
      await fetch(`${stubUrl}/_control/set`, {
        method: 'POST',
        body: JSON.stringify({ address, txs, balance_sats }),
      });
    },
    async setMode(mode) {
      await fetch(`${stubUrl}/_control/set`, {
        method: 'POST',
        body: JSON.stringify({ mode }),
      });
    },
    close: () => new Promise(r => server.close(r)),
  };
}
