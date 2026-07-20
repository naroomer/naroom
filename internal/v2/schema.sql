-- NA Room V2 schema - isolated from V1 tables.
-- All tables and indexes use the v2_ prefix.
-- Plain wallet addresses and raw management codes are never stored.

-- One row per client payment intent / paid listing container.
CREATE TABLE IF NOT EXISTS v2_client_flows (
    id                   TEXT PRIMARY KEY,
    -- HMAC-SHA256(hmac_key, "naroom:v2:wallet:" + currency + ":" + normalized_address)
    wallet_fingerprint   TEXT NOT NULL,
    currency             TEXT NOT NULL CHECK (currency IN ('BTC', 'LTC')),
    -- HMAC-SHA256(hmac_key, "naroom:v2:management-code:" + raw_code); raw code never stored
    management_code_hash TEXT NOT NULL,
    state                TEXT NOT NULL DEFAULT 'awaiting_payment'
                              CHECK (state IN ('awaiting_payment', 'payment_confirmed',
                                               'paid_low_balance', 'form_ready')),
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL
);

-- One invoice per flow (UNIQUE enforces 1:1).
-- Monetary payment amounts use INTEGER (no REAL) to avoid floating-point errors.
-- payment_address, amount_atomic, amount_usd_cents form the invoice snapshot that
-- the watcher and the restore path both need; they are set at creation and never change.
--
-- State-field consistency is enforced by a single multi-column CHECK constraint
-- covering all four status values and their exact valid field configurations.
-- The two allowed forms of 'expired' are also distinguished:
--   (a) expired from pending  — no detected fields set
--   (b) expired from payment_detected — detected fields set, no payment/entitlement fields
CREATE TABLE IF NOT EXISTS v2_invoices (
    id                      TEXT PRIMARY KEY,
    flow_id                 TEXT NOT NULL UNIQUE REFERENCES v2_client_flows(id),
    status                  TEXT NOT NULL DEFAULT 'pending'
                                 CHECK (status IN ('pending', 'payment_detected', 'confirmed', 'expired')),
    -- Invoice snapshot (set at creation, stable thereafter)
    payment_address         TEXT NOT NULL,    -- platform receive address; non-empty
    amount_usd_cents        INTEGER NOT NULL CHECK (amount_usd_cents > 0),
    amount_atomic           INTEGER NOT NULL CHECK (amount_atomic > 0),
    -- Detection deadline: set at creation to created_at + 3600; never changes
    detection_deadline_at   INTEGER NOT NULL, -- unix seconds; created_at + 3600
    -- Detection fields: set when a suitable unconfirmed tx is first seen
    detected_txid           TEXT,             -- NULL until payment_detected
    payment_detected_at     INTEGER,          -- unix seconds; NULL until payment_detected
    -- Confirmation grace deadline: set when detected; NULL until payment_detected
    confirmation_deadline_at INTEGER,         -- payment_detected_at + 86400; NULL until detected
    -- Payment confirmation fields
    payment_txid            TEXT,             -- NULL until payment confirmed; set = detected_txid at confirmation
    payment_confirmed_at    INTEGER,          -- unix seconds; NULL until confirmed
    entitlement_expires_at  INTEGER,          -- confirmed_at + 5*24*3600; NULL until confirmed
    -- Balance check fields (REAL is intentional: wallet balance is a reading, not a payment amount)
    last_balance_usd        REAL,             -- most recent post-payment balance reading
    last_balance_checked_at INTEGER,          -- when last_balance_usd was recorded
    created_at              INTEGER NOT NULL,
    updated_at              INTEGER NOT NULL,

    -- Deadline must be strictly after creation time.
    CHECK (detection_deadline_at > created_at),

    -- Balance pair: both present or both absent.
    CHECK ((last_balance_usd IS NULL) = (last_balance_checked_at IS NULL)),

    -- State-field consistency across all four statuses.
    -- Each status has a precise definition of which fields must be NULL / NOT NULL.
    CHECK (
        -- pending: no detected, payment, or entitlement fields set.
        (status = 'pending'
            AND detected_txid IS NULL
            AND payment_detected_at IS NULL
            AND confirmation_deadline_at IS NULL
            AND payment_txid IS NULL
            AND payment_confirmed_at IS NULL
            AND entitlement_expires_at IS NULL)

        OR

        -- payment_detected: detected snapshot complete; payment/entitlement absent.
        (status = 'payment_detected'
            AND detected_txid IS NOT NULL
            AND payment_detected_at IS NOT NULL
            AND confirmation_deadline_at IS NOT NULL
            AND payment_txid IS NULL
            AND payment_confirmed_at IS NULL
            AND entitlement_expires_at IS NULL)

        OR

        -- confirmed: full detected + payment snapshot; entitlement set;
        -- payment_txid must equal detected_txid (set once at confirmation).
        (status = 'confirmed'
            AND detected_txid IS NOT NULL
            AND payment_detected_at IS NOT NULL
            AND confirmation_deadline_at IS NOT NULL
            AND payment_txid IS NOT NULL
            AND payment_confirmed_at IS NOT NULL
            AND entitlement_expires_at IS NOT NULL
            AND payment_txid = detected_txid)

        OR

        -- expired: no payment/entitlement fields.
        -- Two valid forms depending on when expiry occurred:
        --   (a) from pending  — detected fields absent
        --   (b) from payment_detected — detected fields present, confirmation deadline present
        (status = 'expired'
            AND payment_txid IS NULL
            AND payment_confirmed_at IS NULL
            AND entitlement_expires_at IS NULL
            AND (
                -- form (a): expired before any detection
                (detected_txid IS NULL
                    AND payment_detected_at IS NULL
                    AND confirmation_deadline_at IS NULL)
                OR
                -- form (b): expired after detection within grace period
                (detected_txid IS NOT NULL
                    AND payment_detected_at IS NOT NULL
                    AND confirmation_deadline_at IS NOT NULL)
            ))
    )
);

CREATE INDEX IF NOT EXISTS idx_v2_flows_code_hash  ON v2_client_flows(management_code_hash);
CREATE INDEX IF NOT EXISTS idx_v2_flows_wallet_fp  ON v2_client_flows(wallet_fingerprint);
CREATE INDEX IF NOT EXISTS idx_v2_flows_state      ON v2_client_flows(state);
CREATE INDEX IF NOT EXISTS idx_v2_invoices_status  ON v2_invoices(status);
CREATE INDEX IF NOT EXISTS idx_v2_invoices_txid    ON v2_invoices(payment_txid)
    WHERE payment_txid IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_v2_invoices_flow_status ON v2_invoices(flow_id, status);

-- One notification binding per flow (UNIQUE enforces 1:1).
-- binding_ref is an opaque reference token with format bnd_<32 lowercase hex chars>.
-- It identifies where to deliver notifications without encoding Telegram or other
-- transport identifiers. Never stored in plaintext in any other table.
-- State transitions: ready → active (consumed by FirstPublish/Reactivate).
-- A flow may have at most one binding at a time; replacement is atomic (delete+insert).
-- window_number tracks which activation window this binding covers.
CREATE TABLE IF NOT EXISTS v2_client_notification_bindings (
    id               TEXT PRIMARY KEY,
    flow_id          TEXT NOT NULL UNIQUE REFERENCES v2_client_flows(id),
    binding_ref      TEXT NOT NULL,
    state            TEXT NOT NULL DEFAULT 'ready'
                          CHECK (state IN ('ready', 'active')),
    window_number    INTEGER NOT NULL CHECK (window_number >= 1),
    verified_at      INTEGER NOT NULL,
    valid_until      INTEGER NOT NULL,
    activated_at     INTEGER,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,

    -- binding_ref must be exactly bnd_ followed by 32 lowercase hex chars.
    -- substr checks the literal prefix; NOT GLOB rejects any char outside [0-9a-f]
    -- in the suffix. GLOB is case-sensitive, so uppercase letters are also rejected.
    CHECK (
        length(binding_ref) = 36
        AND substr(binding_ref, 1, 4) = 'bnd_'
        AND NOT (substr(binding_ref, 5) GLOB '*[^0-9a-f]*')
    ),

    -- Each binding_ref is globally unique across all flows.
    UNIQUE(binding_ref),

    -- State-specific timestamp invariants.
    -- ready: no activated_at; window must still be open at creation time.
    -- active: activated_at present, ordered, and within the window;
    --         activated_at must not exceed updated_at.
    CHECK (
        (state = 'ready'
            AND activated_at IS NULL
            AND verified_at < valid_until
            AND created_at < valid_until)
        OR
        (state = 'active'
            AND activated_at IS NOT NULL
            AND verified_at <= activated_at
            AND activated_at < valid_until
            AND activated_at <= updated_at)
    ),

    -- Cross-field timestamp ordering.
    CHECK (verified_at <= created_at),
    CHECK (valid_until > created_at),
    CHECK (updated_at >= created_at)
);

-- One listing per flow (UNIQUE enforces 1:1).
-- languages is canonical JSON (sorted, lowercase, no duplicates), e.g. '["en","ru"]'.
-- contact_ciphertext/nonce/key_version store the AES-GCM encrypted contact; plaintext is never stored.
-- display_name is unique across all listings (UNIQUE enforces global collision-free names).
-- State machine: visible → hidden (daily window expired, entitlement live)
--                visible/hidden → finished (entitlement expired)
-- visible_until is non-NULL only when state='visible'; NULL for hidden/finished.
-- entitlement_expires_at must exceed created_at (enforced by CHECK).
CREATE TABLE IF NOT EXISTS v2_listings (
    id                     TEXT PRIMARY KEY,
    flow_id                TEXT NOT NULL UNIQUE REFERENCES v2_client_flows(id),
    city                   TEXT NOT NULL,
    country_code           TEXT NOT NULL,
    dependency_type        TEXT NOT NULL,
    help_type              TEXT NOT NULL,
    urgency                TEXT NOT NULL,
    languages              TEXT NOT NULL,  -- canonical JSON
    display_name           TEXT NOT NULL UNIQUE,
    contact_type           TEXT NOT NULL CHECK (contact_type IN ('telegram', 'signal')),
    contact_ciphertext     TEXT NOT NULL,
    contact_nonce          TEXT NOT NULL,
    contact_key_version    TEXT NOT NULL,
    state                  TEXT NOT NULL DEFAULT 'visible'
                                CHECK (state IN ('visible', 'hidden', 'finished')),
    visible_until          INTEGER,        -- NULL when hidden or finished
    first_published_at     INTEGER NOT NULL,
    last_activated_at      INTEGER NOT NULL,
    entitlement_expires_at INTEGER NOT NULL,
    activation_count       INTEGER NOT NULL DEFAULT 1 CHECK (activation_count >= 1),
    created_at             INTEGER NOT NULL,
    updated_at             INTEGER NOT NULL,

    -- Non-empty encrypted fields.
    CHECK (length(display_name) > 0),
    CHECK (length(contact_ciphertext) > 0),
    CHECK (length(contact_nonce) > 0),
    CHECK (length(contact_key_version) > 0),

    -- visible_until presence must match state.
    CHECK (
        (state = 'visible'  AND visible_until IS NOT NULL)
        OR
        (state = 'hidden'   AND visible_until IS NULL)
        OR
        (state = 'finished' AND visible_until IS NULL)
    ),

    -- Temporal ordering invariants.
    CHECK (created_at <= first_published_at),
    CHECK (updated_at >= created_at),
    CHECK (first_published_at <= last_activated_at),
    CHECK (last_activated_at <= entitlement_expires_at),
    CHECK (entitlement_expires_at > created_at),

    -- When visible: activation window must be within entitlement.
    CHECK (state != 'visible' OR (last_activated_at < visible_until AND visible_until <= entitlement_expires_at))
);

CREATE INDEX IF NOT EXISTS idx_v2_bindings_flow   ON v2_client_notification_bindings(flow_id);
CREATE INDEX IF NOT EXISTS idx_v2_bindings_state  ON v2_client_notification_bindings(state);
CREATE INDEX IF NOT EXISTS idx_v2_bindings_window ON v2_client_notification_bindings(flow_id, window_number);
CREATE INDEX IF NOT EXISTS idx_v2_listings_flow  ON v2_listings(flow_id);
CREATE INDEX IF NOT EXISTS idx_v2_listings_state ON v2_listings(state);
CREATE INDEX IF NOT EXISTS idx_v2_listings_city  ON v2_listings(city);

-- One link attempt per flow (UNIQUE on flow_id).
-- token_hash: HMAC-SHA256 of raw 43-char base64url token; raw token never stored.
-- state: pending (no lease) or processing (lease active).
-- window_number: which publication window this attempt targets.
CREATE TABLE IF NOT EXISTS v2_telegram_link_attempts (
    id             TEXT PRIMARY KEY,
    flow_id        TEXT NOT NULL UNIQUE REFERENCES v2_client_flows(id),
    token_hash     TEXT NOT NULL UNIQUE,
    window_number  INTEGER NOT NULL CHECK (window_number >= 1),
    state          TEXT NOT NULL DEFAULT 'pending'
                        CHECK (state IN ('pending', 'processing')),
    expires_at     INTEGER NOT NULL,
    lease_until    INTEGER,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,

    -- token_hash must be exactly 64 lowercase hex chars
    CHECK (length(token_hash) = 64 AND NOT (token_hash GLOB '*[^0-9a-f]*')),
    -- pending: no lease; processing: lease present and updated_at within lease
    CHECK (
        (state = 'pending' AND lease_until IS NULL)
        OR
        (state = 'processing' AND lease_until IS NOT NULL AND updated_at <= lease_until)
    ),
    CHECK (created_at < expires_at),
    CHECK (updated_at >= created_at)
);

-- One encrypted destination per binding_ref (1:1 with binding).
-- Deleting the binding cascades to delete the destination.
-- Stores AES-256-GCM encrypted decimal chat_id; plaintext never stored.
-- No flow_id, wallet, management code, Telegram username, or permanent Client identity.
CREATE TABLE IF NOT EXISTS v2_telegram_destinations (
    binding_ref        TEXT PRIMARY KEY
                           REFERENCES v2_client_notification_bindings(binding_ref) ON DELETE CASCADE,
    chat_id_ciphertext TEXT NOT NULL,
    chat_id_nonce      TEXT NOT NULL,
    key_version        TEXT NOT NULL,
    created_at         INTEGER NOT NULL,
    expires_at         INTEGER NOT NULL,

    CHECK (length(chat_id_ciphertext) > 0),
    CHECK (length(chat_id_nonce) > 0),
    CHECK (length(key_version) > 0),
    CHECK (created_at < expires_at)
);

CREATE INDEX IF NOT EXISTS idx_v2_attempts_flow    ON v2_telegram_link_attempts(flow_id);
CREATE INDEX IF NOT EXISTS idx_v2_attempts_state   ON v2_telegram_link_attempts(state);
CREATE INDEX IF NOT EXISTS idx_v2_destinations_ref ON v2_telegram_destinations(binding_ref);

-- ─────────────────────────────────────────────────────────────────────────────
-- Helper contact purchase tables (Task 05)
-- ─────────────────────────────────────────────────────────────────────────────

-- v2_helper_profiles: one row per unique Helper wallet.
-- wallet_fingerprint: HMAC-SHA256("naroom:v2:helper-wallet:"+currency+":"+normalized_addr).
-- public_name: randomly generated once; no wallet/timestamp/counter fragments.
-- country_code: NULL until first successful purchase; locked atomically by CAS.
CREATE TABLE IF NOT EXISTS v2_helper_profiles (
    id                 TEXT PRIMARY KEY,
    wallet_fingerprint TEXT NOT NULL UNIQUE,
    currency           TEXT NOT NULL CHECK (currency IN ('BTC', 'LTC')),
    public_name        TEXT NOT NULL UNIQUE,
    country_code       TEXT,
    purchase_count     INTEGER NOT NULL DEFAULT 0 CHECK (purchase_count >= 0),
    positive_count     INTEGER NOT NULL DEFAULT 0 CHECK (positive_count >= 0),
    negative_count     INTEGER NOT NULL DEFAULT 0 CHECK (negative_count >= 0),
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL,
    -- id: 64 lowercase hex
    CHECK (length(id) = 64 AND NOT (id GLOB '*[^0-9a-f]*')),
    -- wallet_fingerprint: 64 lowercase hex
    CHECK (length(wallet_fingerprint) = 64 AND NOT (wallet_fingerprint GLOB '*[^0-9a-f]*')),
    -- public_name non-empty
    CHECK (length(public_name) > 0),
    -- country_code: NULL or exactly 2 uppercase ASCII letters
    CHECK (country_code IS NULL OR (length(country_code) = 2 AND NOT (country_code GLOB '*[^A-Z]*'))),
    CHECK (updated_at >= created_at)
);

-- v2_helper_purchases: one row per Helper contact purchase attempt.
-- browser_token_hash: HMAC-SHA256("naroom:v2:helper-browser-token:"+raw_token); raw token never stored.
-- State machine: awaiting_payment → payment_detected → payment_confirmed
--               → paid_low_balance (balance < $1000) or contact_ready (balance >= $1000)
--               invoice_expired: invoice expired before payment
--               failed: retry deadline passed while paid_low_balance
--               receipt_expired: receipt window closed
CREATE TABLE IF NOT EXISTS v2_helper_purchases (
    id                        TEXT PRIMARY KEY,
    listing_id                TEXT NOT NULL REFERENCES v2_listings(id),
    helper_profile_id         TEXT NOT NULL REFERENCES v2_helper_profiles(id),
    browser_token_hash        TEXT NOT NULL UNIQUE,
    state                     TEXT NOT NULL DEFAULT 'awaiting_payment'
                                   CHECK (state IN (
                                       'awaiting_payment', 'payment_detected',
                                       'payment_confirmed', 'paid_low_balance',
                                       'contact_ready', 'failed',
                                       'invoice_expired', 'receipt_expired'
                                   )),
    country_code_snapshot     TEXT NOT NULL
                                   CHECK (length(country_code_snapshot) = 2
                                          AND NOT (country_code_snapshot GLOB '*[^A-Z]*')),
    contact_ready_at          INTEGER,
    first_revealed_at         INTEGER,
    receipt_expires_at        INTEGER,
    balance_retry_deadline_at INTEGER,
    last_balance_usd          REAL CHECK (last_balance_usd IS NULL OR (last_balance_usd >= 0 AND last_balance_usd < 1e15)),
    last_balance_checked_at   INTEGER,
    created_at                INTEGER NOT NULL,
    updated_at                INTEGER NOT NULL,

    -- id: 64 lowercase hex
    CHECK (length(id) = 64 AND NOT (id GLOB '*[^0-9a-f]*')),
    -- browser_token_hash: 64 lowercase hex
    CHECK (length(browser_token_hash) = 64 AND NOT (browser_token_hash GLOB '*[^0-9a-f]*')),
    -- balance pair: both present or both absent
    CHECK ((last_balance_usd IS NULL) = (last_balance_checked_at IS NULL)),
    -- reveal pair: receipt_expires_at present IFF first_revealed_at present
    CHECK ((first_revealed_at IS NULL) = (receipt_expires_at IS NULL)),
    -- receipt_expires_at > first_revealed_at (exactly 24h more)
    CHECK (first_revealed_at IS NULL OR receipt_expires_at > first_revealed_at),
    -- first_revealed_at >= contact_ready_at
    CHECK (first_revealed_at IS NULL OR contact_ready_at IS NULL OR first_revealed_at >= contact_ready_at),
    -- receipt_expires_at = first_revealed_at + 86400 (enforced here as range check)
    CHECK (receipt_expires_at IS NULL OR (receipt_expires_at > first_revealed_at AND receipt_expires_at <= first_revealed_at + 86401)),
    -- contact_ready_at only set in contact_ready or receipt_expired
    CHECK (contact_ready_at IS NULL OR state IN ('contact_ready', 'receipt_expired')),
    -- balance_retry_deadline_at only after payment confirmation
    CHECK (balance_retry_deadline_at IS NULL
        OR state IN ('payment_confirmed', 'paid_low_balance', 'contact_ready', 'failed', 'receipt_expired')),
    -- first_revealed_at only in contact_ready or receipt_expired
    CHECK (first_revealed_at IS NULL OR state IN ('contact_ready', 'receipt_expired')),
    -- paid_low_balance requires balance < 1000 and balance_retry_deadline_at
    CHECK (state != 'paid_low_balance' OR (
        last_balance_usd IS NOT NULL AND last_balance_usd < 1000
        AND balance_retry_deadline_at IS NOT NULL
    )),
    -- contact_ready requires balance >= 1000, contact_ready_at, balance_retry_deadline_at
    CHECK (state != 'contact_ready' OR (
        last_balance_usd IS NOT NULL AND last_balance_usd >= 1000
        AND contact_ready_at IS NOT NULL
        AND balance_retry_deadline_at IS NOT NULL
    )),
    CHECK (updated_at >= created_at)
);

-- v2_helper_invoices: immutable $10 payment snapshot for each purchase.
-- amount_usd_cents is exactly 1000. detected_txid_hash: HMAC of raw txid; raw txid never stored.
CREATE TABLE IF NOT EXISTS v2_helper_invoices (
    id                       TEXT PRIMARY KEY,
    purchase_id              TEXT NOT NULL UNIQUE REFERENCES v2_helper_purchases(id),
    status                   TEXT NOT NULL DEFAULT 'pending'
                                  CHECK (status IN ('pending', 'payment_detected', 'confirmed', 'expired')),
    payment_address          TEXT NOT NULL CHECK (length(payment_address) > 0),
    amount_usd_cents         INTEGER NOT NULL CHECK (amount_usd_cents = 1000),
    amount_atomic            INTEGER NOT NULL CHECK (amount_atomic > 0),
    detection_deadline_at    INTEGER NOT NULL,
    detected_txid_hash       TEXT,
    payment_detected_at      INTEGER,
    confirmation_deadline_at INTEGER,
    confirmed_at             INTEGER,
    created_at               INTEGER NOT NULL,
    updated_at               INTEGER NOT NULL,

    -- id: 64 lowercase hex
    CHECK (length(id) = 64 AND NOT (id GLOB '*[^0-9a-f]*')),
    -- purchase_id: 64 lowercase hex
    CHECK (length(purchase_id) = 64 AND NOT (purchase_id GLOB '*[^0-9a-f]*')),
    CHECK (detection_deadline_at > created_at),
    CHECK (updated_at >= created_at),
    -- payment_detected_at <= confirmation_deadline_at when both present
    CHECK (payment_detected_at IS NULL OR confirmation_deadline_at IS NULL OR payment_detected_at <= confirmation_deadline_at),
    -- confirmed_at >= payment_detected_at when both present
    CHECK (payment_detected_at IS NULL OR confirmed_at IS NULL OR confirmed_at >= payment_detected_at),
    -- detected_txid_hash: 64 lowercase hex when set
    CHECK (detected_txid_hash IS NULL OR (
        length(detected_txid_hash) = 64
        AND NOT (detected_txid_hash GLOB '*[^0-9a-f]*')
    )),
    -- State-field consistency
    CHECK (
        (status = 'pending'
            AND detected_txid_hash IS NULL
            AND payment_detected_at IS NULL
            AND confirmation_deadline_at IS NULL
            AND confirmed_at IS NULL)
        OR
        (status = 'payment_detected'
            AND detected_txid_hash IS NOT NULL
            AND payment_detected_at IS NOT NULL
            AND confirmation_deadline_at IS NOT NULL
            AND confirmed_at IS NULL)
        OR
        (status = 'confirmed'
            AND detected_txid_hash IS NOT NULL
            AND payment_detected_at IS NOT NULL
            AND confirmation_deadline_at IS NOT NULL
            AND confirmed_at IS NOT NULL)
        OR
        (status = 'expired'
            AND confirmed_at IS NULL
            AND (
                (detected_txid_hash IS NULL AND payment_detected_at IS NULL AND confirmation_deadline_at IS NULL)
                OR
                (detected_txid_hash IS NOT NULL AND payment_detected_at IS NOT NULL AND confirmation_deadline_at IS NOT NULL)
            ))
    )
);

CREATE INDEX IF NOT EXISTS idx_v2_helper_profiles_fp       ON v2_helper_profiles(wallet_fingerprint);
CREATE INDEX IF NOT EXISTS idx_v2_helper_purchases_profile ON v2_helper_purchases(helper_profile_id);
CREATE INDEX IF NOT EXISTS idx_v2_helper_purchases_listing ON v2_helper_purchases(listing_id);
CREATE INDEX IF NOT EXISTS idx_v2_helper_purchases_state   ON v2_helper_purchases(state);
CREATE INDEX IF NOT EXISTS idx_v2_helper_invoices_status   ON v2_helper_invoices(status);
CREATE INDEX IF NOT EXISTS idx_v2_helper_invoices_purchase ON v2_helper_invoices(purchase_id);
