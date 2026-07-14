PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA synchronous = NORMAL;

-- Объявления
CREATE TABLE IF NOT EXISTS listings (
    id TEXT PRIMARY KEY,
    city TEXT NOT NULL,
    dependency_type TEXT NOT NULL,
    help_type TEXT NOT NULL,
    urgency TEXT NOT NULL,
    languages TEXT NOT NULL,
    wallet_hash TEXT NOT NULL,         -- HMAC-SHA256(HASH_KEY, address); plain address never stored
    payment_txid TEXT,
    visible_until INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    status TEXT DEFAULT 'active',
    is_sample INTEGER DEFAULT 0,        -- 1 = demo listing shown to new visitors
    renewal_count INTEGER DEFAULT 0,    -- how many times renewed
    first_activated_at INTEGER,         -- set on first payment (retained for analytics; not used for renewal gating)
    opened_chats_count INTEGER NOT NULL DEFAULT 0  -- number of paid chat_rooms created for this listing (max 2)
);

-- Отклики психологов
CREATE TABLE IF NOT EXISTS responses (
    id TEXT PRIMARY KEY,
    listing_id TEXT NOT NULL REFERENCES listings(id),
    counselor_hash TEXT NOT NULL,      -- HMAC-SHA256(salt, counselor_address)
    counselor_pubkey TEXT NOT NULL,
    status TEXT DEFAULT 'pending',
    created_at INTEGER NOT NULL,
    cancelled_at INTEGER,
    cooldown_until INTEGER
);

-- Платежи
CREATE TABLE IF NOT EXISTS invoices (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    address TEXT NOT NULL,
    amount_usd REAL NOT NULL,
    amount_crypto TEXT,
    currency TEXT NOT NULL,
    payer_address TEXT,    -- HMAC-SHA256 hash of sender wallet (plain address never stored)
    txid TEXT,
    status TEXT DEFAULT 'pending',
    listing_id TEXT,      -- для type=listing: что активировать после оплаты
    response_id TEXT,     -- для type=chat: откуда брать counselor
    client_pubkey TEXT,   -- для type=chat: pubkey клиента для E2E
    chat_room_id TEXT,         -- заполняется после создания комнаты (защита от дублей)
    payment_detected_at INTEGER, -- set when payment tx found; extends expiry +24h if API is down
    price_at_creation REAL,      -- USD/coin rate at invoice creation; used at confirmation if favorable
    created_at INTEGER NOT NULL
);

-- Кошельки (активные сессии)
-- wallet_address is NOT stored in plain text. Only wallet_hash (HMAC) and
-- wallet_address_enc (AES-256-GCM) are persisted. Decrypt only for blockchain API calls.
CREATE TABLE IF NOT EXISTS wallet_sessions (
    wallet_hash        TEXT PRIMARY KEY,  -- HMAC-SHA256(HASH_KEY, address); plain address never stored here
    wallet_address_enc TEXT NOT NULL,     -- AES-256-GCM encrypted address (nonce||ciphertext, base64url)
    currency           TEXT NOT NULL DEFAULT 'BTC',  -- BTC or LTC; stored to avoid decrypting for type detection
    role               TEXT NOT NULL,
    balance_status     TEXT DEFAULT 'ok',
    min_required_usd   REAL NOT NULL,
    balance_usd        REAL DEFAULT 0,
    last_checked_at    INTEGER,
    low_since          INTEGER,
    verified           BOOLEAN DEFAULT FALSE,
    first_seen         INTEGER NOT NULL,
    created_at         INTEGER NOT NULL
);

-- Рейтинг (агрегаты по хешу)
CREATE TABLE IF NOT EXISTS reputation (
    counselor_hash TEXT PRIMARY KEY,
    region TEXT NOT NULL,
    sessions_total INTEGER DEFAULT 0,
    sessions_completed INTEGER DEFAULT 0,
    sessions_early_exit INTEGER DEFAULT 0,
    thumbs_up INTEGER DEFAULT 0,
    thumbs_down INTEGER DEFAULT 0,
    returning_clients INTEGER DEFAULT 0,
    first_seen INTEGER NOT NULL
);

-- Review токены (анонимные, одноразовые)
CREATE TABLE IF NOT EXISTS review_tokens (
    token TEXT PRIMARY KEY,
    counselor_hash TEXT NOT NULL,
    is_paid BOOLEAN DEFAULT FALSE,
    used BOOLEAN DEFAULT FALSE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);

-- Abuse counters
CREATE TABLE IF NOT EXISTS abuse_counters (
    client_hash TEXT PRIMARY KEY,
    abuse_misuse INTEGER DEFAULT 0,
    abuse_threatening INTEGER DEFAULT 0,
    abuse_drugs INTEGER DEFAULT 0,
    abuse_links INTEGER DEFAULT 0,
    abuse_other INTEGER DEFAULT 0,
    total INTEGER DEFAULT 0,
    banned_until INTEGER
);

-- Дедупликация abuse reports
CREATE TABLE IF NOT EXISTS abuse_dedup (
    pair_hash TEXT PRIMARY KEY,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);

-- Чат-комнаты
CREATE TABLE IF NOT EXISTS chat_rooms (
    id TEXT PRIMARY KEY,
    listing_id TEXT REFERENCES listings(id),
    response_id TEXT REFERENCES responses(id),
    client_hash TEXT NOT NULL,         -- HMAC-SHA256(salt, client_address)
    counselor_hash TEXT NOT NULL,      -- HMAC-SHA256(salt, counselor_address)
    client_pubkey TEXT NOT NULL,
    counselor_pubkey TEXT NOT NULL,
    started_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    closed_at INTEGER,
    closed_by TEXT,
    peer_left_at INTEGER,
    client_left_at INTEGER,
    status TEXT DEFAULT 'active',
    listing_counted INTEGER NOT NULL DEFAULT 0  -- 1 = opened_chats_count already incremented for this room
);

-- Зашифрованные сообщения (TTL 24ч)
CREATE TABLE IF NOT EXISTS encrypted_messages (
    id TEXT PRIMARY KEY,
    room_id TEXT REFERENCES chat_rooms(id),
    sender_pubkey TEXT NOT NULL,
    nonce TEXT NOT NULL,
    ciphertext TEXT NOT NULL,
    msg_type TEXT NOT NULL DEFAULT 'text',  -- text | image_file | image_camera
    created_at INTEGER NOT NULL
);


-- Telegram one-time link tokens. Bind Telegram chat_id to a listing or helper filters.
-- wallet_address is never stored here. counselor_hash is stored for helper tokens only:
-- it allows the "chat opened" notification to locate the helper's Telegram chat_id.
-- This is an intentional opt-in: the helper explicitly links Telegram and thereby consents
-- to having their counselor_hash associated with their Telegram chat_id for direct notifications.
CREATE TABLE IF NOT EXISTS telegram_link_tokens (
    id TEXT PRIMARY KEY,
    token TEXT NOT NULL UNIQUE,
    token_type TEXT NOT NULL CHECK (token_type IN ('client', 'helper')),
    listing_id TEXT REFERENCES listings(id),
    helper_filters_json TEXT,
    counselor_hash TEXT,           -- helper tokens only: HMAC-SHA256 of helper wallet; enables direct chat notifications
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,   -- 10 minutes
    used BOOLEAN DEFAULT FALSE
);

-- Client notification binding: one per listing lifecycle. No wallet fields.
CREATE TABLE IF NOT EXISTS client_listing_notifications (
    id TEXT PRIMARY KEY,
    listing_id TEXT NOT NULL REFERENCES listings(id),
    telegram_chat_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,   -- matches listing visible_until (6h)
    active BOOLEAN DEFAULT TRUE
);

-- Helper board subscription: 24h window.
-- counselor_hash is stored to enable direct "chat opened" notifications.
-- Helpers who link Telegram opt into this association; no wallet_address is stored.
CREATE TABLE IF NOT EXISTS helper_board_subscriptions (
    id TEXT PRIMARY KEY,
    telegram_chat_id TEXT NOT NULL,
    counselor_hash TEXT,           -- HMAC-SHA256 of helper wallet; nullable (older subscriptions may lack it)
    city TEXT,
    language TEXT,
    problem TEXT,
    help_type TEXT,
    urgency TEXT,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,   -- 24h from creation
    active BOOLEAN DEFAULT TRUE
);

-- Счётчик индексов для HD wallet (BTC/LTC)
CREATE TABLE IF NOT EXISTS invoice_index (
    currency TEXT PRIMARY KEY,
    next_index INTEGER DEFAULT 0
);

-- Stable principal identity, decoupled from wallet address.
-- wallet_hash here is the billing wallet (nullable until first payment).
CREATE TABLE IF NOT EXISTS principals (
    id            TEXT PRIMARY KEY,      -- random 256-bit opaque identifier
    recovery_hash TEXT NOT NULL UNIQUE,  -- HMAC(HASH_KEY, raw_recovery_code); raw code never stored
    wallet_hash   TEXT,                  -- billing wallet_hash (set/updated after wallet registration)
    currency      TEXT,                  -- BTC or LTC (billing currency)
    role          TEXT NOT NULL,         -- client or peer
    created_at    INTEGER NOT NULL,
    last_seen     INTEGER
);

-- Authenticated sessions (issued after wallet signature verification)
-- wallet_address is NOT stored here — only wallet_hash (HMAC-SHA256 of address).
-- Plain wallet address lives only in wallet_sessions (needed for blockchain API calls).
CREATE TABLE IF NOT EXISTS sessions (
    token_hash    TEXT PRIMARY KEY,
    wallet_hash   TEXT NOT NULL,   -- HMAC-SHA256(HASH_KEY, address); plain address never stored
    currency      TEXT NOT NULL,
    role          TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    last_seen_at  INTEGER,
    revoked_at    INTEGER
);

-- Индексы
CREATE INDEX IF NOT EXISTS idx_listings_city_status ON listings(city, status);
CREATE INDEX IF NOT EXISTS idx_listings_visible ON listings(visible_until);
CREATE INDEX IF NOT EXISTS idx_responses_listing ON responses(listing_id);
CREATE INDEX IF NOT EXISTS idx_responses_counselor ON responses(counselor_hash);
CREATE INDEX IF NOT EXISTS idx_wallet_sessions_hash ON wallet_sessions(wallet_hash);
CREATE INDEX IF NOT EXISTS idx_listings_wallet_hash ON listings(wallet_hash);
CREATE INDEX IF NOT EXISTS idx_chat_rooms_client_hash ON chat_rooms(client_hash);
CREATE INDEX IF NOT EXISTS idx_chat_rooms_counselor_hash ON chat_rooms(counselor_hash);
CREATE INDEX IF NOT EXISTS idx_invoices_status ON invoices(status);
CREATE INDEX IF NOT EXISTS idx_chat_rooms_status ON chat_rooms(status);
CREATE INDEX IF NOT EXISTS idx_chat_rooms_expires ON chat_rooms(expires_at);
CREATE INDEX IF NOT EXISTS idx_encrypted_messages_room ON encrypted_messages(room_id);
CREATE INDEX IF NOT EXISTS idx_encrypted_messages_created ON encrypted_messages(created_at);
CREATE INDEX IF NOT EXISTS idx_review_tokens_expires ON review_tokens(expires_at);
CREATE INDEX IF NOT EXISTS idx_abuse_dedup_expires ON abuse_dedup(expires_at);
CREATE INDEX IF NOT EXISTS idx_sessions_wallet  ON sessions(wallet_hash, role);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_telegram_link_tokens_token   ON telegram_link_tokens(token);
CREATE INDEX IF NOT EXISTS idx_telegram_link_tokens_expires ON telegram_link_tokens(expires_at);
CREATE INDEX IF NOT EXISTS idx_client_notifications_listing ON client_listing_notifications(listing_id, active, expires_at);
CREATE INDEX IF NOT EXISTS idx_client_notifications_expires ON client_listing_notifications(expires_at);
CREATE INDEX IF NOT EXISTS idx_helper_subs_active           ON helper_board_subscriptions(active, expires_at);
-- Only one active subscription per Telegram chat_id
CREATE UNIQUE INDEX IF NOT EXISTS idx_helper_subs_active_chat
    ON helper_board_subscriptions(telegram_chat_id) WHERE active = TRUE;
