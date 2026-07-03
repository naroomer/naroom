// 027_challenge_replay.js — ownership proof at registration
//
// ВАЖНО (см. аудит): механизма challenge/verify в коде НЕТ.
//   - /wallet/register выдаёт session_token только по адресу + балансу,
//     БЕЗ подписи и без challenge (register.go: "No signature required").
//   - Таблица wallet_challenges была удалена (db.go: DROP TABLE wallet_challenges),
//     она никогда не была подключена ни к одному хендлеру.
//   - Поскольку wallet_hash = HMAC(HASH_KEY, address) детерминирован, а
//     доказательства владения нет, знание чужого профинансированного адреса
//     даёт session с ЧУЖИМ wallet_hash → доступ к session-gated эндпоинтам
//     жертвы (список откликов, метаданные комнат, close, abuse-report).
//
// Поэтому тест НЕ на «replay challenge» (тестировать нечего), а на само
// целевое свойство: регистрация без доказательства владения адресом должна
// быть НЕВОЗМОЖНА. Пока это свойство нарушено — тест КРАСНЫЙ намеренно,
// как открытая находка. Крипто-часть (одноразовость/TTL/привязка к адресу)
// покрывается Go-unit-тестом challenge-стора — см. go_tests/wallet_challenge_test.go.
//
// testwallets.js НЕ импортируется: ставить в дерево непроверяемую JS-подпись
// secp256k1 хуже, чем один honest-red тест + Go-unit там, где подпись уже решена
// (internal/crypto/verify_test.go использует btcec).

import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { Runner } from '../lib/runner.js';

// Реальный адрес, «принадлежащий жертве» (в devMode баланс не проверяется —
// это лишь усиливает точку: в dev вообще любой адрес проходит).
const VICTIM_WALLET = '1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf';

export async function run() {
  console.log('\n=== 027: Registration ownership proof (challenge/verify) ===');
  const srv = new TestServer();
  const t = new Runner('027_challenge_replay');

  try {
    await srv.start();
    const api = new ApiClient(srv.base);

    // Пробуем challenge-эндпоинт. Если его нет — это и есть находка.
    await t.run('challenge endpoint exists', async () => {
      const r = await fetch(`${srv.base}/wallet/challenge`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ address: VICTIM_WALLET }),
      });
      if (r.status === 404) {
        throw new Error(
          'НАХОДКА (открыта): /wallet/challenge отсутствует. Регистрация не требует ' +
          'доказательства владения адресом → возможна имперсонация по чужому wallet_hash. ' +
          'Реализовать challenge+verify (подпись поверх серверного nonce; verify.go уже умеет ' +
          'проверять BTC/LTC-подписи), затем включить блок ниже.'
        );
      }
    });

    // ── Пост-реализация: раскомментировать, когда challenge/verify появятся.
    // Свойства проверяются Go-unit-тестом; здесь — HTTP-контур:
    //   (a) один challenge не проходит дважды (replay -> 409/410)
    //   (b) истёкший challenge отклоняется
    //   (c) challenge адреса A нельзя предъявить для адреса B
    // Для валидной подписи в Node используйте тот же путь, что verify_test.go
    // (btcec compact sig), либо вызывайте маленький Go-хелпер-подписант.

  } finally {
    await srv.stop();
  }

  return t.summary();
}

run().then(ok => { process.exit(ok ? 0 : 1); }).catch(e => { console.error(e); process.exit(1); });
