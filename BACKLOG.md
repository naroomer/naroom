# NA Room — Backlog

Задачи, отложенные на потом. Когда делается — переносить в DONE.md.

---

## Приоритет 1 — Запуск (нужно до первых пользователей)

- [ ] **Реальный домен** — заменить `naroom.io` в `sitemap.xml`, `robots.txt`, `llms.txt` на финальный домен; убрать хардкод `naroom.net` из challenge.go и сделать через env `DOMAIN`
- [ ] **GitHub репозиторий** — создать `github.com/naroom` (или другой URL), заменить плейсхолдер в `hiw.a5`, `llms.txt`, `SECURITY.md`, `SELF_HOSTING.md`
- [ ] **E2E тесты после рефакторинга** — запустить `e2e/tests/001–014`; все хеши теперь HMAC-SHA256 + новая схема sessions (без wallet_address) — нужно убедиться что 14/14 проходят
- [ ] **Удалить dev DB перед тестами** — смена алгоритма хеширования делает старые хеши невалидными; удалить `naroom.db` и пересоздать

---

## Приоритет 2 — Качество / Безопасность

- [x] **`wallet_sessions.wallet_address_enc` — AES-256-GCM encrypt at rest** ✅ DONE (Sprint 1). Plain address encrypted before INSERT; decrypted only inside balance/payment workers. `wallet_challenges` table dropped entirely.
- [ ] **Blockchain API через Tor** — сейчас mempool.space и BlockCypher видят IP сервера + запросы по адресам пользователей. В production — роутить через Tor (SOCKS5 прокси в Go HTTP клиенте)
- [ ] **Safety code в чате** — короткий код из 12 символов `SHA256(room_id + client_pubkey + counselor_pubkey)` показывать обоим участникам для проверки E2E ключей. Защита от MITM при компрометации сервера.
- [ ] **BIP-322** — новый формат подписи Bitcoin; добавить после тестирования с реальными кошельками (legacy P2PKH+P2WPKH покрывает все основные)

---

## Приоритет 3 — UX / Контент

- [ ] **og:image** — добавить мета-тег в `layout.svelte` для превью в соцсетях / мессенджерах
- [ ] **Vietnamese (vi)** — города Нячанг и Да Нанг есть, переводов нет; добавить `vi` в i18n.js
- [ ] **Search Console** — после запуска зарегистрировать в Google Search Console и Yandex Webmaster, подать sitemap

---

## Приоритет 4 — Производство (после первых пользователей)

- [ ] **Развёртывание** — privacy VPS (Njalla / BuyVM / 1984 Hosting), оплата крипто; конфиг по `SELF_HOSTING.md`
- [ ] **Tor hidden service** — `.onion` адрес как основной способ доступа; `torrc` конфиг
- [ ] **TLS для clearnet** — Let's Encrypt через certbot; nginx без access_log
- [ ] **systemd unit** — supervision процесса, автозапуск
- [ ] **SERVER_SALT + HASH_KEY** — только в env переменных, никогда в файлах; swap отключён

---

## Известные допустимые риски (не баги, документированы)

- `wallet_sessions.wallet_address_enc` — AES-256-GCM encrypted. Если у атакующего есть и БД и `WALLET_ENC_KEY` — адреса открываются. Держи ключ отдельно от БД. Задокументировано в `THREAT_MODEL.md`.
- Listing metadata (город, тип, время) — plain text. Операционная необходимость.
- Blockchain payment graph — публичен. Пользователи предупреждены в UI и `DATA_RETENTION.md`.
- Frontend JS delivery trust — документировано в `SECURITY.md`.
