// 029_ciphertext_only.js — сервер видит только шифротекст
//
// ИСПРАВЛЕНО против первой версии:
//   - НЕ нужен e2e/lib/db.js: у TestServer уже есть srv.db(sql) (sqlite3 CLI).
//   - НЕ нужен e2e/lib/e2e_crypto.js и импорт крипты фронтенда: реальный
//     E2E-пайплайн — это nacl.box (X25519 + XSalsa20-Poly1305, НЕ AES-GCM,
//     как ошибочно говорил брифинг). Он уже реализован в e2e/lib/crypto.js
//     и это ровно та же схема, что использует фронтенд (tweetnacl).
//   - Сообщение отправляется через реальный транспорт (/chat/poll/send),
//     так что в encrypted_messages попадает ровно то, что в проде.
//
// Свойство: канареечный plaintext не восстановим из БД (ни из колонок
// encrypted_messages, ни из сырого файла БД, включая WAL) без клиентских ключей.
//
// Полностью запускается на существующей инфраструктуре, ADAPT не осталось.

import { readFileSync, existsSync } from 'node:fs';
import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { newKeypair, encrypt, decrypt } from '../lib/crypto.js';
import { assertStatus, pollUntil } from '../lib/assert.js';
import { Runner } from '../lib/runner.js';

const CLIENT_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';
const PEER_WALLET   = '1BoatSLRHtKNngkdXEeobR76b53LETtpyT';

// Уникальная канарейка: невозможна как случайная последовательность байт.
const CANARY = 'CANARY_7f3a91e2_do_not_store_plaintext_privet_e2e';

// Ищет канарейку в буфере: как есть, и после hex/base64-декодирования
// (на случай, если что-то по ошибке сложило plaintext в закодированном виде).
function findPlaintext(buf, canary) {
  const raw = buf.toString('utf8');
  if (raw.includes(canary)) return 'raw';
  const compact = raw.replace(/\s+/g, '');
  try {
    if (Buffer.from(compact, 'base64').toString('utf8').includes(canary)) return 'base64';
  } catch {}
  try {
    if (Buffer.from(compact, 'hex').toString('utf8').includes(canary)) return 'hex';
  } catch {}
  return null;
}

export async function run() {
  console.log('\n=== 029: Ciphertext Only (server never sees plaintext) ===');
  const srv = new TestServer(); // devMode=true: invoices auto-confirm, no balance API
  const t = new Runner('029_ciphertext_only');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);
    const clientKeys = newKeypair();
    const peerKeys   = newKeypair();
    let listingId, responseId, roomId;

    // ── Setup: довести до активного чата (сжатый happy-path) ──────────────
    await t.run('setup: register client + peer', async () => {
      assertStatus(await api.verifyWallet(CLIENT_WALLET, 'BTC', 'client'), 200, 'client register');
      assertStatus(await api.verifyWallet(PEER_WALLET,   'BTC', 'peer'),   200, 'peer register');
    });

    await t.run('setup: create listing -> active', async () => {
      const r = await api.createListing(CLIENT_WALLET);
      assertStatus(r, 201, 'create listing');
      listingId = r.body.listing_id;
      await pollUntil(async () => {
        const s = await api.getListing(listingId);
        return s.body.status === 'active' ? true : null;
      }, { timeout: 30000, label: 'listing active' });
    });

    await t.run('setup: peer responds', async () => {
      const r = await api.respond(listingId, PEER_WALLET, peerKeys.pub);
      assertStatus(r, 201, 'respond');
      responseId = r.body.response_id;
    });

    await t.run('setup: client accepts -> chat room opens', async () => {
      assertStatus(
        await api.acceptResponse(responseId, CLIENT_WALLET, clientKeys.pub),
        200, 'accept'
      );
      const room = await pollUntil(async () => {
        const r = await api.getPeerChatroom(PEER_WALLET, listingId);
        return r.status === 200 ? r.body : null;
      }, { timeout: 30000, label: 'chat room open' });
      roomId = room.room_id;
    });

    // ── Отправить канарейку через РЕАЛЬНЫЙ E2E-пайплайн ───────────────────
    await t.run('send canary through real nacl.box pipeline', async () => {
      const enc = encrypt(CANARY, clientKeys.priv, peerKeys.pub); // {nonce, ciphertext} hex
      const r = await api.pollSend(roomId, CLIENT_WALLET, clientKeys.pub, enc.nonce, enc.ciphertext, 'text');
      assertStatus(r, 201, 'poll send canary');
    });

    // Санити: peer действительно расшифровывает — доказывает, что мы гоняем
    // настоящую крипту, а не пустышку (иначе тест мог бы «пройти» на нулях).
    await t.run('sanity: peer decrypts canary back to plaintext', async () => {
      const r = await api.pollReceive(roomId, PEER_WALLET, peerKeys.pub, 0);
      assertStatus(r, 200, 'poll receive');
      const msg = (r.body.messages || []).find(m => m.ciphertext);
      if (!msg) throw new Error('no message came back from server');
      const plain = decrypt(msg.nonce, msg.ciphertext, peerKeys.priv, clientKeys.pub);
      if (plain !== CANARY) throw new Error(`peer decrypt mismatch: got ${JSON.stringify(plain)}`);
    });

    // ── 1. Колонки encrypted_messages не содержат plaintext ───────────────
    await t.run('DB columns: no plaintext in encrypted_messages', async () => {
      const rows = srv.db(
        `SELECT nonce || '|' || ciphertext || '|' || sender_pubkey || '|' || msg_type ` +
        `FROM encrypted_messages WHERE room_id='${roomId}'`
      );
      if (!rows) throw new Error('encrypted_messages empty — message never persisted');
      const hit = findPlaintext(Buffer.from(rows), CANARY);
      if (hit) throw new Error(`PLAINTEXT в encrypted_messages (кодировка: ${hit})`);
      const ctLen = srv.db(`SELECT MAX(LENGTH(ciphertext)) FROM encrypted_messages WHERE room_id='${roomId}'`);
      if (parseInt(ctLen, 10) < 32) throw new Error(`ciphertext подозрительно короткий: ${ctLen}`);
    });

    // ── 2. Сырой файл БД (+ WAL/SHM) не содержит plaintext ────────────────
    await t.run('raw DB file (incl. WAL): no plaintext anywhere', async () => {
      for (const suffix of ['', '-wal', '-shm']) {
        const p = srv.dbPath + suffix;
        if (!existsSync(p)) continue;
        const hit = findPlaintext(readFileSync(p), CANARY);
        if (hit) throw new Error(`PLAINTEXT в файле ${p.split('/').pop()} (кодировка: ${hit})`);
      }
    });

    // Примечание: приватность ЛОГОВ отдельно покрыта 024_log_privacy.

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
