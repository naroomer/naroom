// 027_challenge_replay
// Свойства:
//   a) один challenge нельзя верифицировать дважды (одноразовость)
//   b) истёкший challenge отклоняется
//   c) challenge, выданный кошельку A, нельзя использовать для кошелька B
//
// ВАЖНО: в текущем DEV_MODE подпись/кошелёк не проверяются вовсе — в таком
// режиме тест бессмыслен. Нужна гранулярность dev-флагов:
//   DEV_SKIP_PAYMENTS=true  (инвойсы автоподтверждаются — оставить)
//   DEV_SKIP_WALLET_VERIFY=false (challenge-логика работает по-настоящему)
// Подпись в тесте делаем реальным ключом (bitcoinjs-message или аналог),
// либо тестовым hook'ом TEST_SIGNATURE_MODE=hmac, где "подпись" =
// HMAC(test_key, challenge) — главное, чтобы проверка одноразовости/TTL/
// привязки к адресу шла по продакшн-коду.

import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';
import { assertStatus } from '../lib/assert.js';
import { signChallenge, WALLET_A, WALLET_B } from '../lib/testwallets.js'; // ADAPT: создать хелпер

export const name = '027_challenge_replay';

export async function run() {
  const server = new TestServer({
    env: {
      DEV_SKIP_PAYMENTS: 'true',
      DEV_SKIP_WALLET_VERIFY: 'false',
      CHALLENGE_TTL_SECONDS: '2', // короткий TTL для пункта (b)
    },
  });
  await server.start();
  const api = new ApiClient(server.url);

  try {
    // --- (a) одноразовость ---
    let res = await api.post('/api/wallet/challenge', { address: WALLET_A.address }); // ADAPT: роут
    assertStatus(res, 200);
    const { challenge } = res.body;

    const sig = await signChallenge(WALLET_A, challenge);
    res = await api.post('/api/wallet/verify', {
      address: WALLET_A.address, challenge, signature: sig,
    });
    assertStatus(res, 200, 'первая верификация должна пройти');

    res = await api.post('/api/wallet/verify', {
      address: WALLET_A.address, challenge, signature: sig,
    });
    if (res.status === 200) {
      throw new Error('REPLAY: тот же challenge принят второй раз');
    }
    // ожидаем 401/409/410 — challenge погашен

    // --- (b) истечение TTL ---
    res = await api.post('/api/wallet/challenge', { address: WALLET_A.address });
    const expired = res.body.challenge;
    await new Promise(r => setTimeout(r, 2500)); // > CHALLENGE_TTL_SECONDS
    res = await api.post('/api/wallet/verify', {
      address: WALLET_A.address,
      challenge: expired,
      signature: await signChallenge(WALLET_A, expired),
    });
    if (res.status === 200) {
      throw new Error('TTL: истёкший challenge принят');
    }

    // --- (c) кросс-кошелёк ---
    res = await api.post('/api/wallet/challenge', { address: WALLET_A.address });
    const chA = res.body.challenge;
    // Кошелёк B подписывает challenge, выданный для A, и предъявляет как свой
    res = await api.post('/api/wallet/verify', {
      address: WALLET_B.address,
      challenge: chA,
      signature: await signChallenge(WALLET_B, chA),
    });
    if (res.status === 200) {
      throw new Error('CROSS-WALLET: challenge кошелька A принят для кошелька B');
    }
    // Требование к реализации: challenge должен храниться вместе с адресом,
    // для которого выдан, и сверяться при verify. Если сейчас challenge
    // "плавающий" (не привязан к адресу) — это и есть баг, который тест ловит.
  } finally {
    await server.stop();
  }
}
