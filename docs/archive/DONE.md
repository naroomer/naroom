> **HISTORICAL / ARCHIVED** — This document records the state as of 2026-06-17 (pre-Sprint 1).
> Architecture has since changed: wallet signature auth (`/wallet/challenge` + `/wallet/verify`) was
> replaced by `/wallet/register` (balance-check based). `wallet_sessions.wallet_address` (plain text)
> was replaced by `wallet_sessions.wallet_address_enc` (AES-256-GCM). `wallet_challenges` table dropped.
> Current architecture is documented in `ARCHITECTURE.md`, `SECURITY.md`, and `THREAT_MODEL.md`.

# NA Room — Security Implementation Complete (HISTORICAL)

Дата: 2026-06-17

## Что реализовано

### Шаг 1 — Rate Limiting
- Middleware `RateLimiter` (token bucket, `golang.org/x/time/rate`)
- Ключ = hashed subnet (/24), IP не хранится
- `/wallet/challenge` 5/мин, `/wallet/verify` 10/мин, `/listing/respond` 3/мин, `/board` 60/мин, `/invoice/status` 30/мин, `/abuse-report` 5/час

### Шаг 2 — Wallet Challenge (server-issued nonce)
- `POST /wallet/challenge` → `{ challenge_id, message, expires_at }` (TTL 5 мин)
- Формат: "NA Room wallet verification\nDomain: naroom.net\nPurpose: anonymous session login..."
- 32-байтовый crypto/rand nonce, запись в `wallet_challenges`

### Шаг 3 — Wallet Signature Verification
- `VerifyBTCMessage` / `VerifyLTCMessage` (legacy format: double SHA256 + varint)
- Поддержка: P2PKH compressed/uncompressed, P2WPKH (bc1/ltc1)
- Покрывает: Electrum, BlueWallet, Ledger
- 7/7 unit-тестов PASS

### Шаг 4 — Session Tokens
- `POST /wallet/verify` → `{ session_token, expires_in: 86400 }`
- Токен: 32 bytes → base64url (43 chars) — возвращается клиенту один раз
- В БД: только SHA256-хеш токена
- Middleware `RequireSession`: `Authorization: Bearer <token>` → wallet+role в context
- `POST /session/refresh` — ротация токена
- `POST /session/revoke` — отзыв
- Dev mode: `X-Dev-Wallet` + `X-Dev-Role` headers вместо токена (только DEV_MODE=true)
- TTL cleaner удаляет истёкшие/отозванные сессии

### Шаг 5 — WS Auth через Sec-WebSocket-Protocol
- Было: `ws://host/chat/ws?room_id=xxx&token=xxx` (токен в URL — bad)
- Стало: `new WebSocket(url, [sessionToken])` → браузер отправляет `Sec-WebSocket-Protocol`
- Бэкенд читает `r.Header.Get("Sec-WebSocket-Protocol")`, эхо-ответ в `AcceptOptions.Subprotocols`
- URL чистый: `/chat/ws?room_id=xxx` — никаких секретов

### Шаг 6 — Аудит фронтенда
- `localStorage` — не используется ✓
- `pubkey=` в URL — не используется ✓
- `wallet_address=` в query — не используется ✓
- Все сессии: `sessionStorage`, все запросы: `Authorization: Bearer`

### Шаг 7 — TTL Cleaner для peer_left
- `expirePeerLeftRooms()` в `worker/ttl_cleaner.go`
- Транзакция: room → expired, messages удалены, listing восстановлен (если visible_until > now)
- Review token не выдаётся — клиент не закрыл явно

### Шаг 8 — Дополнительные дыры
- **Abuse report**: counselor из сессии, `room_id` доказывает участие → 403 если не в комнате
- **Invoice scoping**: `GET /invoice/{id}/status` требует сессии + проверяет владельца
- **counselor_address в responses**: убран из API ответа — клиент видит "Peer #1" вместо адреса

---

## Архитектура аутентификации (итог)

```
Wallet                  Server
  │                       │
  │── POST /wallet/challenge ──►│  creates nonce (5 min TTL)
  │◄── { challenge_id, message }│
  │                       │
  │  [sign message in wallet]    │
  │                       │
  │── POST /wallet/verify ────►│  verify sig → create session
  │◄── { session_token }  │
  │                       │
  │── Authorization: Bearer ──►│  all protected endpoints
  │   OR                  │
  │── new WebSocket(url, [token])│  WS auth via Sec-WebSocket-Protocol
```

---

## Файлы изменены

**Backend:**
- `internal/middleware/ratelimit.go` — rate limiter
- `internal/middleware/session.go` — RequireSession middleware
- `internal/handler/challenge.go` — wallet challenge
- `internal/handler/wallet.go` — verify + session issue
- `internal/handler/session.go` — refresh + revoke
- `internal/handler/listing.go` — session-based auth, убран peer_address из responses
- `internal/handler/respond.go` — session-based auth
- `internal/handler/accept.go` — session-based auth
- `internal/handler/renew.go` — session-based auth
- `internal/handler/chat_ws.go` — WS auth via Sec-WebSocket-Protocol
- `internal/handler/abuse.go` — room participation guard
- `internal/handler/invoice.go` — session + ownership check
- `internal/worker/ttl_cleaner.go` — peer_left expiry, session/challenge cleanup
- `internal/crypto/verify.go` — BTC/LTC signature verification
- `internal/crypto/verify_test.go` — 7 unit tests
- `internal/db/schema.sql` — sessions, wallet_challenges tables
- `cmd/naroom/main.go` — routes, session middleware

**Frontend:**
- `frontend2/src/routes/new/+page.svelte` — session flow
- `frontend2/src/routes/listing/[id]/+page.svelte` — session headers, peer #N display
- `frontend2/src/routes/chat/[room_id]/+page.svelte` — WS via Sec-WebSocket-Protocol

**Tests:**
- `e2e/lib/http.js` — session-aware ApiClient
- `e2e/lib/ws.js` — ChatWS via Sec-WebSocket-Protocol
- `e2e/tests/001-013` — полный тест-сьют (13 сьютов)

---

## Известные ограничения

- `listings.wallet_address` и `chat_rooms.client_address/counselor_address` хранятся в plain text.
  Замена на hash требует передачи server salt в воркеры `balance_checker` и `invoice_watcher`.
  Адреса НЕ раскрываются в API ответах. Отдельная задача.

- BIP-322 (новый формат подписи Bitcoin) не реализован.
  Legacy signing покрывает все основные кошельки (Electrum, BlueWallet, Ledger).
  Добавить после тестирования с реальными кошельками.

---

## Test 014 — Peer Reputation (2026-06-20)

Написан и отлажен тест `e2e/tests/014_reputation.js`. **Итог: 14/14 сьютов, все проходят.**

### Что тестирует 014:
- API responses содержит поле `reputation` с правильными типами
- Новый peer: `is_new=true`, 0 сессий, 0 оценок
- `balance_tier` считается из баланса ($1000→1, $2500→2, $3100→3)
- `member_since` — валидный timestamp
- После сессии: `sessions_completed` растёт
- `thumbs_up` / `thumbs_down` инкрементируются через review_token
- После 5+ сессий: `is_new=false`
- Данные из API совпадают с БД

### Баги найдены и исправлены:

**1. Дедлок в `GetListingResponses` (главная причина зависания)**
- `db.SetMaxOpenConns(1)` — одно соединение
- Открытый cursor `rows` его держал
- Внутри цикла `QueryRow` для reputation ждал соединения → вечный дедлок
- Фикс: сначала все строки в память (`raw []rawResponse`), закрываем `rows`, потом QueryRow

**2. `INVOICE_WATCH_INTERVAL` = 30s в тест-сервере (почему казалось "завис")**
- Каждый `fullSession` ждал 2×30s = 60s
- 5+ сессий = 5 минут без вывода
- Фикс: `INVOICE_WATCH_INTERVAL: '2'` в `e2e/lib/server.js`

**3. Rate limiter `3/min` блокировал respond после 3 вызовов**
- Тест делает много `respond` быстро
- Фикс: в DevMode все rate limiters через `middleware.NoLimit` (кроме `/wallet/challenge` — его тест 007 проверяет)

**4. TTL cleaner удалял wallet_sessions агрессивно**
- Удалял если нет активных листингов/комнат — но reputation query LEFT JOIN wallet_sessions для balance_tier
- После удаления: `balance_usd=0` → `balance_tier=0`
- Фикс: удалять wallet_sessions только если нет живого Bearer token (привязка к `sessions` table)

**5. `clearListings` не закрывала `pending`/`matched` листинги**
- Следующий `createListing` падал с 409
- Фикс: `status IN ('active', 'pending', 'matched')`

### Файлы изменены:
- `internal/handler/listing.go` — дедлок фикс (rows → raw slice → close → QueryRow)
- `internal/worker/ttl_cleaner.go` — wallet_sessions живут пока есть auth session
- `internal/middleware/ratelimit.go` — добавлен `NoLimit` key function
- `cmd/naroom/main.go` — DevMode: `rateFn=NoLimit`, challenge всегда `ByIP`
- `e2e/lib/server.js` — `INVOICE_WATCH_INTERVAL: '2'`
- `e2e/tests/014_reputation.js` — `clearListings` закрывает active+pending+matched

---

## Документация + UI Privacy Table + Логирование (2026-06-21)

### Документация (4 новых файла)

- `SECURITY.md` — что защищаем / что не защищаем / честный claim / как репортить уязвимости
- `THREAT_MODEL.md` — таблицы: что знает оператор / взломщик без ключа / взломщик с ключом / blockchain API / наблюдатель
- `DATA_RETENTION.md` — полная таблица данных (хранится / не хранится / формат / когда удаляется) + объяснение "anonymous" на языке платформы
- `SELF_HOSTING.md` — переменные окружения, генерация секретов (`openssl rand -hex 32`), hardening сервера (ufw, fail2ban, swap off, core dumps off), systemd unit, Tor hidden service (`torrc`), Nginx (no access logs, strip IP), политика логов, список "никогда не делай"

### UI — how-it-works (все 4 языка)

Добавлена секция **"What we know about you"** с цветной таблицей:

| Цвет | Значение | Примеры |
|------|----------|---------|
| Зелёный | Never | IP, identity, analytics |
| Accent | Hash / E2E | wallet address, messages, session token |
| Оранжевый | Plain text | listing metadata (city, type, time) |
| Предупреждение | Blockchain | payment transaction |

Примечание внизу таблицы: "Blockchain payments are pseudonymous, not anonymous."

Ключи i18n добавлены в EN / RU / ES / KA: `hiw.privacy_title` ... `hiw.privacy_note` (23 ключа × 4 языка).

### Логирование — убраны идентификаторы

`internal/middleware/nolog.go` теперь логирует **паттерн маршрута** вместо реального пути:

```
// Было:
POST /listing/lst_abc123def456 200 12ms

// Стало:
POST /listing/{id} 200 12ms
```

Через `chi.RouteContext(r.Context()).RoutePattern()` — никаких ID, room_id, wallet addresses в логах.

### Файлы изменены

- `SECURITY.md` — новый
- `THREAT_MODEL.md` — новый
- `DATA_RETENTION.md` — новый
- `SELF_HOSTING.md` — новый
- `frontend2/src/routes/how-it-works/+page.svelte` — privacy table секция + стили
- `frontend2/src/lib/i18n.js` — 23 новых ключа × 4 языка
- `internal/middleware/nolog.go` — route pattern logging

---

## Codex Security Review — Fixes (2026-06-21)

Реализованы все критические замечания из Codex review.

### HMAC-SHA256 вместо SHA256(salt+address)

**Было:** `SHA256(salt + address)` через `crypto.Hash(salt, address)` — подвержен length extension attacks, нестандартная конструкция.

**Стало:** `HMAC-SHA256(key, "naroom:v1:" + normalize(address))` через `crypto.WalletHash(key, address)` — стандартная keyed hash, правильная конструкция.

**Нормализация адресов:** bech32 адреса (`bc1...`, `ltc1...`) приводятся к lowercase перед хешированием. Legacy адреса сохраняются как есть (case-sensitive).

### Отдельный HASH_KEY

- Новая переменная окружения `HASH_KEY` — для HMAC кошельков.
- Если не задана — fallback на `SERVER_SALT` (обратная совместимость).
- Лучше задавать оба: `SERVER_SALT` (описание контекста) + `HASH_KEY` (ключ хешей).

### wallet_address убран из sessions

**Было:** `sessions` хранил `wallet_address TEXT NOT NULL` — live сервер и взломщик с БД знали все адреса активных пользователей.

**Стало:** `sessions` хранит только `wallet_hash TEXT NOT NULL`. Plain адрес остался только в `wallet_sessions` (нужен для blockchain API).

### Middleware переписан

- `SessionWallet()` → `SessionWalletHash()` — отдаёт хеш, не адрес.
- `RequireSession(db, devMode)` → `RequireSession(db, devMode, hashKey []byte)`.
- Dev mode shortcut: `X-Dev-Wallet` хешируется с `WalletHash(hashKey, wallet)` перед записью в контекст.
- WS auth (`Sec-WebSocket-Protocol`): читает `wallet_hash` из sessions, не `wallet_address`.

### Hub keyed by wallet_hash

`ChatHub.rooms[roomID]` было `map[wallet_address → conn]`. Стало `map[wallet_hash → conn]`. Plain адрес больше нигде не хранится в памяти.

### Handler рефакторинг

Все handlers теперь:
```go
// Было:
walletAddress := middleware.SessionWallet(r.Context())
walletHash := crypto.Hash(h.Salt, walletAddress)

// Стало:
walletHash := middleware.SessionWalletHash(r.Context())
```

Wallet address больше нигде не извлекается из контекста сессии.

### Файлы изменены

- `internal/crypto/id.go` — `WalletHash()` + `NormalizeAddress()`, `Hash()` оставлен для non-wallet
- `internal/config/config.go` — `HashKey []byte`, fallback на `SERVER_SALT`
- `internal/handler/handler.go` — `Salt string` → `HashKey []byte`
- `internal/middleware/session.go` — `SessionWalletHash()`, `RequireSession` с `hashKey`
- `internal/db/schema.sql` — `wallet_address` убран из `sessions`; комментарии обновлены
- `internal/handler/wallet.go` — `issueSession(walletHash, ...)`, `WalletHash` везде
- `internal/handler/session.go` — refresh читает `wallet_hash`, `SessionWalletHash` в revoke
- `internal/handler/listing.go` — `SessionWalletHash`, убраны все `Hash(h.Salt, ...)`
- `internal/handler/respond.go` — `SessionWalletHash`, `wallet_hash` в balance check
- `internal/handler/accept.go` — `SessionWalletHash`
- `internal/handler/renew.go` — `SessionWalletHash`
- `internal/handler/chat_ws.go` — полный рефакторинг hub + handlers
- `internal/handler/abuse.go` — `SessionWalletHash`
- `internal/handler/invoice.go` — `SessionWalletHash`, убран `crypto` import
- `internal/worker/balance_checker.go` — `Salt` → `HashKey`, `WalletHash`
- `internal/worker/invoice_watcher.go` — `Salt` → `HashKey`
- `cmd/naroom/main.go` — `HashKey` везде, `RequireSession` с `cfg.HashKey`

**Результат:** `go build ./...` и `go test ./...` — чисто.

---

## Хеширование адресов кошельков (2026-06-21)

Plain адреса кошельков убраны из всех таблиц кроме оперативных (`wallet_sessions`, `sessions`, `wallet_challenges`).

### Что изменено в схеме БД:

| Таблица | Было | Стало |
|---------|------|-------|
| `listings` | `wallet_address TEXT` | `wallet_hash TEXT` |
| `responses` | `counselor_address TEXT` | `counselor_hash TEXT` |
| `chat_rooms` | `client_address TEXT` | `client_hash TEXT` |
| `chat_rooms` | `counselor_address TEXT` | `counselor_hash TEXT` |
| `wallet_sessions` | — | добавлен `wallet_hash TEXT` |
| `chat_rooms` | — | добавлен `peer_left_at INTEGER` (был в коде, отсутствовал в схеме) |

Хеш: `SHA256(salt + address)` через `crypto.Hash(salt, address)`.

### Что изменено в коде:

**Handlers:**
- `listing.go` — ownership через `wallet_hash`, JOIN с wallet_sessions через `wallet_hash`, INSERT хранит хеш
- `renew.go` — ownership через `wallet_hash`
- `respond.go` — все запросы по `counselor_hash`, INSERT хранит хеш
- `accept.go` — ownership через `client_hash`, проверка активных чатов через `client_hash`
- `chat_ws.go` — `ChatWS`, `GetChatRoom`, `GetCounselorChatRoom`, `CloseChat` — все сравнения через хеш
- `abuse.go` — participation guard через `counselor_hash`, `client_hash` берётся напрямую из chat_rooms
- `invoice.go` — ownership invoice через `wallet_hash`
- `wallet.go` — `upsertWalletSession` сохраняет `wallet_hash` в wallet_sessions

**Workers:**
- `balance_checker.go` — добавлен `Salt string`; `closeChatsAndListings` ищет по `client_hash`/`counselor_hash`/`wallet_hash`
- `invoice_watcher.go` — добавлен `Salt string`; `confirmInvoice` (тип `chat`) читает хеши из responses/listings и записывает в chat_rooms

**main.go:** `Salt: cfg.ServerSalt` передан в `BalanceChecker` и `InvoiceWatcher`.

### Что осталось plain (намеренно):
- `wallet_sessions.wallet_address` — нужен для blockchain API (mempool.space, blockcypher)
- `sessions.wallet_address` — нужен для сессионного middleware
- `wallet_challenges.wallet_address` — нужен для верификации подписи

### Результат:
При утечке БД без серверного `SERVER_SALT` — адреса кошельков в листингах, откликах и чат-комнатах не восстановимы. Связь между активностью пользователя и его идентичностью разорвана.
