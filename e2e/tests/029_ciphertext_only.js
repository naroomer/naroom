// 029_ciphertext_only
// Свойство: содержимое чата в БД и логах — только шифротекст. Plaintext
// невозможно найти ни в сыром виде, ни после декодирования base64/hex.
//
// Канареечная строка должна быть уникальной и невозможной как случайное
// совпадение байтов.
//
// ADAPT (ключевое): сообщение должно пройти через РЕАЛЬНЫЙ клиентский
// E2E-пайплайн (X25519 + AES-256-GCM). Если слать plaintext прямо в API —
// тест проверит не то. Два варианта:
//   1. Вынести crypto-модуль фронтенда так, чтобы его можно было импортировать
//      из Node-теста (лучший вариант, заодно юнит-тестируется сам модуль).
//   2. Playwright: два реальных клиента в браузере обмениваются сообщением.
// Ниже — вариант 1.

import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { assertStatus } from '../lib/assert.js';
import { openDb } from '../lib/db.js'; // ADAPT: хелпер чтения sqlite (better-sqlite3)
import { createChatSession, e2eEncrypt } from '../lib/e2e_crypto.js'; // ADAPT: обёртка над frontend/src/lib/crypto
import fs from 'node:fs';

export const name = '029_ciphertext_only';

const CANARY = 'CANARY-7f3a91-нельзя-читать-этот-текст-e2e';

function containsPlaintext(buf, canary) {
  const raw = buf.toString('utf8');
  if (raw.includes(canary)) return 'raw';
  // возможные кодировки хранения
  try {
    const b64 = Buffer.from(raw.replace(/\s/g, ''), 'base64').toString('utf8');
    if (b64.includes(canary)) return 'base64';
  } catch {}
  try {
    const hex = Buffer.from(raw.replace(/\s/g, ''), 'hex').toString('utf8');
    if (hex.includes(canary)) return 'hex';
  } catch {}
  return null;
}

export async function run() {
  const server = new TestServer();
  await server.start();
  const api = new ApiClient(server.url);

  try {
    // ADAPT: полный сетап до рабочего чата — как шаги 1-6 в 001_happy_path:
    // кошелёк клиента → объявление → кошелёк peer'а → отклик → accept → chat room.
    const { roomId, clientSession, peerSession } = await setupChat(api);

    // Реальный E2E-хэндшейк и отправка через WS (или тот же транспорт, что фронт)
    const chat = await createChatSession(server.url, roomId, clientSession, peerSession);
    await chat.send(CANARY);
    await chat.waitDelivered();

    // --- 1. БД: все сообщения комнаты ---
    const db = openDb(server.dbPath);
    const rows = db.prepare(
      'SELECT * FROM chat_messages WHERE room_id = ?' // ADAPT: таблица/колонки
    ).all(roomId);
    if (rows.length === 0) throw new Error('сообщение не дошло до БД — тест не о том');

    for (const row of rows) {
      for (const [col, val] of Object.entries(row)) {
        if (val == null) continue;
        const found = containsPlaintext(Buffer.from(String(val)), CANARY);
        if (found) {
          throw new Error(`PLAINTEXT В БД: chat_messages.${col} содержит канарейку (кодировка: ${found})`);
        }
      }
    }

    // --- 2. Вся БД целиком (вдруг plaintext осел в другой таблице/индексе) ---
    const dbBytes = fs.readFileSync(server.dbPath);
    const foundInDb = containsPlaintext(dbBytes, CANARY);
    if (foundInDb) {
      throw new Error(`PLAINTEXT В ФАЙЛЕ БД (вне chat_messages, кодировка: ${foundInDb})`);
    }

    // --- 3. Логи сервера ---
    const logs = fs.readFileSync(server.logPath, 'utf8'); // ADAPT: где TestServer копит stdout/stderr
    if (containsPlaintext(Buffer.from(logs), CANARY)) {
      throw new Error('PLAINTEXT В ЛОГАХ СЕРВЕРА');
    }

    // Санити-проверка самого теста: убедимся, что шифртекст в БД вообще есть
    // (иначе можно "пройти" тест на пустых данных при ошибке транспорта).
    const anyBlob = rows.some(r =>
      Object.values(r).some(v => typeof v === 'string' && v.length > 32)
    );
    if (!anyBlob) throw new Error('в chat_messages нет блобов — что тогда хранится?');
  } finally {
    await server.stop();
  }
}

// ADAPT: реализовать по образцу 001
async function setupChat(api) {
  throw new Error('setupChat: скопировать шаги из 001_happy_path');
}
