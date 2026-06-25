> **HISTORICAL / ARCHIVED** — This document was the working implementation plan as of 2026-06-17.
> Some items are now superseded: wallet signature auth has been removed, `wallet_sessions.wallet_address`
> (plain text) replaced with `wallet_sessions.wallet_address_enc` (AES-256-GCM), `wallet_challenges`
> table dropped. Current security status is in `SECURITY.md`, `THREAT_MODEL.md`, and `docs/INVARIANTS.md`.

# NA Room — Security Implementation Plan (HISTORICAL)

Создан: 2026-06-17
Статус обновляется по мере реализации. ✅ = сделано, 🔄 = в процессе, ⬜ = не начато.

---

## Принципы (не обсуждаются)

- Это не MVP. Каждая найденная дыра — приоритет.
- Zero accounts: кошелёк = идентичность.
- Zero metadata: адреса хешируются с server salt перед хранением.
- E2E encryption: сервер никогда не видит plaintext.
- Эфемерность: сообщения удаляются после закрытия комнаты, chat_rooms — через 48h.
- Одно устройство на сессию: ключи генерируются в браузере, намеренно не портируются.

---

## Что уже реализовано (до этого плана)

✅ Полный E2E флоу (листинг → инвойс → отклик → акцепт → чат → закрытие)
✅ NaCl box E2E шифрование (X25519 + XSalsa20-Poly1305)
✅ WebSocket чат с историей
✅ peer_left статус: пир уходит → клиент получает WS system event → закрывает вручную
✅ WS system messages (peer_left, room_closed)
✅ Восстановление листинга на доске после закрытия чата
✅ Отзыв 👍/👎 только для клиента (role guard на фронте и бэке)
✅ Кастомный модал закрытия (не нативный confirm)
✅ Dust payment guard (99% min в satoshis)
✅ Race condition на respond — транзакция
✅ Accept — атомарный UPDATE + транзакция
✅ Invoice confirmation — одна транзакция
✅ Body size limits (64KB / 8MB для изображений)
✅ chat_rooms удаляются через 48h после закрытия
✅ Scope GetCounselorChatRoom по listing_id (защита от stale room)
✅ E2E тест-сьют: 6 сьютов, 73+ проверки — все проходят
✅ dev.sh: одна команда для перезапуска бэкенда с чистой БД

---

## План реализации (в порядке зависимостей)

### Шаг 1 — Rate Limiting ✅

**Цель:** защита от флуда до появления аутентификации.

Библиотека: `golang.org/x/time/rate` (token bucket).

Лимиты:
```
POST /wallet/challenge     — 5/мин/IP, 20/час/wallet_hash
POST /wallet/verify        — 10/мин/IP, 10/мин/wallet_hash
POST /listing/{id}/respond — 3/мин/сессия, 1/листинг/сессия, 20/день/wallet_hash
POST /abuse-report         — 5/час/сессия, требует участия в комнате
GET  /board/{city}         — 60/мин/IP или сессия
GET  /invoice/:id/status   — 30/мин/сессия
```

Реализация:
- ✅ Middleware `RateLimiter` с функцией ключа `RateKeyFunc`
- ✅ До аутентификации: ключ = hashed(RemoteAddr), не логируем IP
- ⬜ После аутентификации: ключ = session token hash / wallet hash (после Шага 4)
- ✅ In-memory map с TTL cleanup, IP не персистируются

---

### Шаг 2 — Wallet Challenge (server-issued nonce) ✅

**Цель:** сервер выдаёт нонс для подписи — защита от replay-атак.

Новая таблица:
```sql
CREATE TABLE wallet_challenges (
  id          TEXT PRIMARY KEY,
  wallet_address TEXT NOT NULL,
  currency    TEXT NOT NULL,
  role        TEXT NOT NULL,
  nonce       TEXT NOT NULL,
  message     TEXT NOT NULL,
  created_at  INTEGER NOT NULL,
  expires_at  INTEGER NOT NULL,  -- now + 5 минут
  used        INTEGER DEFAULT 0
);
CREATE INDEX idx_wallet_challenges_wallet
  ON wallet_challenges(wallet_address, used, expires_at);
```

Новый эндпоинт:
```
POST /wallet/challenge
  { wallet_address, currency, role }
→ { challenge_id, message, expires_at }
```

Формат сообщения для подписи:
```
NA Room wallet verification

Domain: naroom.net
Purpose: anonymous session login
Wallet: <address>
Role: <client|peer>
Challenge: <random_32_bytes_base64>
Issued At: <ISO8601>
Expires At: <ISO8601>
```

Реализация:
- ✅ `POST /wallet/challenge` handler
- ✅ Запись challenge в БД с TTL 5 минут
- ✅ Генерация случайного 32-байтового nonce (crypto/rand)
- ✅ TTL cleaner удаляет истёкшие challenges

---

### Шаг 3 — Wallet Signature Verification ✅

**Цель:** криптографическое доказательство владения кошельком.

Интерфейс:
```go
type WalletVerifier interface {
    Verify(address, message, signature string) error
}
```

BTC:
- ✅ Legacy Bitcoin message signing (P2PKH + P2WPKH) — покрывает Electrum, BlueWallet, Ledger
- ⬜ BIP-322 simple/full — добавить после тестирования с реальными кошельками

LTC:
- ✅ Legacy Litecoin message signing (те же алгоритмы, другие network params)
- ⬜ BIP-322 для LTC — после тестирования совместимости

Обновлённый `POST /wallet/verify`:
```
{ challenge_id, wallet_address, currency, role, signature }
```
- ✅ Загрузить challenge по id, проверить не истёк и not used
- ✅ Диспатч по currency на BTC/LTC verifier
- ✅ Отметить challenge used=1
- ⬜ Создать сессию (см. Шаг 4)

Unit-тесты: 7/7 PASS (P2PKH, P2WPKH, LTC P2PKH, wrong address, wrong message, invalid base64, short sig)

---

### Шаг 4 — Session Tokens ✅

**Цель:** заменить wallet_address как bearer credential на одноразовый токен.

Новая таблица:
```sql
CREATE TABLE sessions (
  token_hash    TEXT PRIMARY KEY,
  wallet_address TEXT NOT NULL,
  wallet_hash   TEXT NOT NULL,
  currency      TEXT NOT NULL,
  role          TEXT NOT NULL,
  created_at    INTEGER NOT NULL,
  expires_at    INTEGER NOT NULL,  -- now + 24h
  last_seen_at  INTEGER,
  revoked_at    INTEGER
);
CREATE INDEX idx_sessions_wallet  ON sessions(wallet_hash, role);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);
```

Генерация токена:
```go
raw := make([]byte, 32)
rand.Read(raw)
token := base64.RawURLEncoding.EncodeToString(raw)
tokenHash := sha256.Sum256([]byte(token))
// вернуть только raw token, хранить только tokenHash
```

Фронтенд:
```js
sessionStorage.setItem(`naroom_session_${role}`, token)
// использовать: Authorization: Bearer <token>
```

Обновление `/wallet/verify`: после верификации подписи → создать сессию → вернуть token.

Новый эндпоинт:
```
POST /session/refresh
Authorization: Bearer <old_token>
→ { token, expires_at }  // ротация токена
```

Middleware `RequireSession`:
- ⬜ Читает `Authorization: Bearer <token>`
- ⬜ Hash → lookup в sessions таблице
- ⬜ Проверяет expires_at, revoked_at
- ⬜ Кладёт wallet_address + role в context

Реализация:
- ✅ Session table + генерация токена в /wallet/verify
- ✅ `RequireSession` middleware
- ✅ `POST /session/refresh` + `POST /session/revoke`
- ✅ TTL cleaner удаляет истёкшие/отозванные сессии
- ✅ Заменить wallet_address/pubkey в request bodies на сессионный контекст
- ✅ Hub хранит WS соединения по wallet_address (не pubkey)
- ✅ ChatWS читает токен из ?token= query param (временно до Шага 5)

---

### Шаг 5 — WS Auth через Session Token ✅

**Цель:** убрать pubkey из URL WebSocket соединения.

Решение — `Sec-WebSocket-Protocol` header:
```js
new WebSocket(url, [sessionToken])
```

Бэкенд читает токен из `r.Header.Get("Sec-WebSocket-Protocol")` при upgrade, эхо-ответом в `AcceptOptions.Subprotocols`.

Реализация:
- ✅ ChatWS handler: читает токен из Sec-WebSocket-Protocol (не ?token=)
- ✅ Верифицирует сессию, достаёт wallet_address, находит pubkey из chat_rooms
- ✅ Фронтенд: `new WebSocket(url, [sessionToken])` — токен как WS subprotocol, URL чистый

---

### Шаг 6 — Убрать pubkey/wallet из URL на фронтенде ✅

Аудит:
- ✅ `localStorage` — не найдено
- ✅ `pubkey=` — не найдено
- ✅ `wallet_address=` — не найдено

Все чувствительные данные передаются через `Authorization: Bearer` или `sessionStorage`.

---

### Шаг 7 — TTL Cleaner для peer_left ✅

Реализация:
- ✅ `expirePeerLeftRooms()` в `worker/ttl_cleaner.go`
- ✅ Транзакция: expire room → delete messages → restore listing (только если visible_until > now)
- ✅ Нет review token — клиент не закрыл сессию явно
- ⬜ E2E тест: создать комнату в peer_left, дождаться expiry, проверить листинг

---

### Шаг 8 — Дополнительные дыры ✅

- ✅ **Abuse report guard**: counselor берётся из сессии, `room_id` доказывает участие в комнате
- ✅ **Invoice scoping**: `/invoice/{id}/status` требует сессии, проверяет владельца через listings
- ✅ **counselor_address в `/listing/{id}/responses`**: убран из ответа (клиенту не нужен адрес пира, только pubkey для E2E)
- ✅ **wallet_address_enc в wallet_sessions**: AES-256-GCM encrypted at rest (Sprint 1). balance_checker/invoice_watcher decrypt only transiently. `wallet_challenges` table dropped. listings/chat_rooms never stored plain addresses — they always used HMAC hashes.

---

## E2E тесты к добавлению

- ⬜ Test 007: rate limiting (превышение лимита → 429)
- ⬜ Test 008: wallet challenge + signature verification
- ⬜ Test 009: session token lifecycle (issue, use, refresh, expire)
- ⬜ Test 010: WS auth через token (не pubkey)
- ⬜ Test 011: peer_left room expires → listing restored
- ⬜ Test 012: abuse report без участия в комнате → 403
- ⬜ Test 013: invoice status без сессии → 403

---

## Как возобновить работу после обрыва сессии

1. Прочитать этот файл: `SECURITY_PLAN.md`
2. Найти первый пункт без ✅
3. Прочитать соответствующий handler/worker файл
4. Реализовать, отметить ✅
5. Запустить `./dev.sh` и E2E тесты

Кошельки для тестирования:
- Клиент (Chrome): `1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf`
- Пир (Brave): `1BoatSLRHtKNngkdXEeobR76b53LETtpyT`
