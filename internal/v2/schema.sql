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
    -- FK to v2_client_profiles; NULL only while state='awaiting_payment'.
    -- Set atomically at first successful $5 payment confirmation.
    -- v2_client_profiles is defined later in this file; SQLite does not validate
    -- the referenced table at DDL time (only at DML time with PRAGMA foreign_keys=ON).
    client_profile_id    TEXT REFERENCES v2_client_profiles(id),
    required_hard_floor_usd REAL NOT NULL DEFAULT 120.0,
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL,
    -- awaiting_payment may have NULL profile; all post-confirm states require one.
    CHECK (state = 'awaiting_payment' OR client_profile_id IS NOT NULL)
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
    display_name           TEXT NOT NULL,
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


-- Helper contact purchase tables (Task 05, hardened Task 05-FIX)

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
    -- id: exactly 64 lowercase hex chars
    CHECK (length(id) = 64 AND NOT (id GLOB '*[^0-9a-f]*')),
    -- wallet_fingerprint: exactly 64 lowercase hex chars
    CHECK (length(wallet_fingerprint) = 64 AND NOT (wallet_fingerprint GLOB '*[^0-9a-f]*')),
    -- public_name non-empty
    CHECK (length(public_name) > 0),
    -- country_code: NULL or exactly 2 uppercase ASCII letters
    CHECK (country_code IS NULL
        OR (length(country_code) = 2 AND NOT (country_code GLOB '*[^A-Z]*'))),
    CHECK (updated_at >= created_at)
);

-- v2_helper_purchases: one row per Helper contact purchase attempt.
-- browser_token_hash: HMAC-SHA256("naroom:v2:helper-browser-token:"+raw_token); raw token never stored.
-- State machine: awaiting_payment -> payment_detected -> payment_confirmed
--               -> paid_low_balance (balance < $1000) or contact_ready (balance >= $1000)
--               invoice_expired: invoice expired before payment
--               failed: country CAS loss or retry deadline passed
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
    country_code_snapshot     TEXT NOT NULL,
    contact_ready_at          INTEGER,
    first_revealed_at         INTEGER,
    receipt_expires_at        INTEGER,
    balance_retry_deadline_at INTEGER,
    last_balance_usd          REAL CHECK (last_balance_usd IS NULL
                                          OR (last_balance_usd >= 0 AND last_balance_usd < 1e15)),
    last_balance_checked_at   INTEGER,
    required_post_payment_floor_usd REAL NOT NULL DEFAULT 1000.0,
    created_at                INTEGER NOT NULL,
    updated_at                INTEGER NOT NULL,

    -- id: exactly 64 lowercase hex chars
    CHECK (length(id) = 64 AND NOT (id GLOB '*[^0-9a-f]*')),
    -- browser_token_hash: exactly 64 lowercase hex chars
    CHECK (length(browser_token_hash) = 64 AND NOT (browser_token_hash GLOB '*[^0-9a-f]*')),
    -- country_code_snapshot: exactly 2 uppercase ASCII letters
    CHECK (length(country_code_snapshot) = 2
           AND NOT (country_code_snapshot GLOB '*[^A-Z]*')),
    -- receipt_expires_at must be exactly first_revealed_at + 86400 (not just > it)
    CHECK (first_revealed_at IS NULL OR receipt_expires_at = first_revealed_at + 86400),
    -- first_revealed_at not before contact_ready_at
    CHECK (first_revealed_at IS NULL OR contact_ready_at IS NULL
           OR first_revealed_at >= contact_ready_at),
    -- Comprehensive state-specific field requirements for all 8 purchase states.
    -- Each state enforces required/forbidden fields; no state can hold fields
    -- that belong only to a later lifecycle phase.
    CHECK (
        -- awaiting_payment: all balance/retry/contact/reveal fields NULL
        (state = 'awaiting_payment'
            AND last_balance_usd IS NULL AND last_balance_checked_at IS NULL
            AND balance_retry_deadline_at IS NULL
            AND contact_ready_at IS NULL
            AND first_revealed_at IS NULL AND receipt_expires_at IS NULL)
        OR
        -- payment_detected: same as awaiting_payment
        (state = 'payment_detected'
            AND last_balance_usd IS NULL AND last_balance_checked_at IS NULL
            AND balance_retry_deadline_at IS NULL
            AND contact_ready_at IS NULL
            AND first_revealed_at IS NULL AND receipt_expires_at IS NULL)
        OR
        -- payment_confirmed: retry deadline required; balance/contact/reveal NULL
        (state = 'payment_confirmed'
            AND balance_retry_deadline_at IS NOT NULL
            AND last_balance_usd IS NULL AND last_balance_checked_at IS NULL
            AND contact_ready_at IS NULL
            AND first_revealed_at IS NULL AND receipt_expires_at IS NULL)
        OR
        -- paid_low_balance: retry deadline + balance pair (< floor); contact/reveal NULL
        (state = 'paid_low_balance'
            AND balance_retry_deadline_at IS NOT NULL
            AND last_balance_usd IS NOT NULL AND last_balance_usd < required_post_payment_floor_usd
            AND last_balance_checked_at IS NOT NULL
            AND contact_ready_at IS NULL
            AND first_revealed_at IS NULL AND receipt_expires_at IS NULL)
        OR
        -- contact_ready: retry deadline + balance (>= floor) + contact_ready_at; reveal pair both or neither
        (state = 'contact_ready'
            AND balance_retry_deadline_at IS NOT NULL
            AND last_balance_usd IS NOT NULL AND last_balance_usd >= required_post_payment_floor_usd
            AND last_balance_checked_at IS NOT NULL
            AND contact_ready_at IS NOT NULL
            AND (first_revealed_at IS NULL) = (receipt_expires_at IS NULL))
        OR
        -- invoice_expired: all balance/retry/contact/reveal fields NULL
        (state = 'invoice_expired'
            AND last_balance_usd IS NULL AND last_balance_checked_at IS NULL
            AND balance_retry_deadline_at IS NULL
            AND contact_ready_at IS NULL
            AND first_revealed_at IS NULL AND receipt_expires_at IS NULL)
        OR
        -- failed: contact/reveal NULL; balance/retry snapshot preserved (optional)
        (state = 'failed'
            AND contact_ready_at IS NULL
            AND first_revealed_at IS NULL AND receipt_expires_at IS NULL)
        OR
        -- receipt_expired: contact_ready_at + balance/retry snapshot required; reveal pair both or neither
        (state = 'receipt_expired'
            AND contact_ready_at IS NOT NULL
            AND balance_retry_deadline_at IS NOT NULL
            AND last_balance_usd IS NOT NULL AND last_balance_usd >= required_post_payment_floor_usd
            AND last_balance_checked_at IS NOT NULL
            AND (first_revealed_at IS NULL) = (receipt_expires_at IS NULL))
    ),
    CHECK (updated_at >= created_at)
);

-- v2_helper_invoices: immutable $10 payment snapshot for each purchase.
-- amount_usd_cents is exactly 1000. detected_txid_hash: HMAC of raw txid; raw txid never stored.
CREATE TABLE IF NOT EXISTS v2_helper_invoices (
    id                       TEXT PRIMARY KEY,
    purchase_id              TEXT NOT NULL UNIQUE REFERENCES v2_helper_purchases(id),
    status                   TEXT NOT NULL DEFAULT 'pending'
                                  CHECK (status IN ('pending', 'payment_detected',
                                                    'confirmed', 'expired')),
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

    -- id: exactly 64 lowercase hex chars
    CHECK (length(id) = 64 AND NOT (id GLOB '*[^0-9a-f]*')),
    -- purchase_id: exactly 64 lowercase hex chars
    CHECK (length(purchase_id) = 64 AND NOT (purchase_id GLOB '*[^0-9a-f]*')),
    CHECK (detection_deadline_at > created_at),
    CHECK (updated_at >= created_at),
    -- confirmation_deadline_at = payment_detected_at + 86400 exactly
    CHECK (payment_detected_at IS NULL OR confirmation_deadline_at IS NULL
           OR confirmation_deadline_at = payment_detected_at + 86400),
    -- confirmed_at must lie within [payment_detected_at, confirmation_deadline_at]
    CHECK (confirmed_at IS NULL OR payment_detected_at IS NULL
           OR (confirmed_at >= payment_detected_at
               AND (confirmation_deadline_at IS NULL
                    OR confirmed_at <= confirmation_deadline_at))),
    -- detected_txid_hash when set must be exactly 64 lowercase hex chars
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
                (detected_txid_hash IS NULL AND payment_detected_at IS NULL
                    AND confirmation_deadline_at IS NULL)
                OR
                (detected_txid_hash IS NOT NULL AND payment_detected_at IS NOT NULL
                    AND confirmation_deadline_at IS NOT NULL)
            ))
    )
);

-- ── Task 06: Client reputation and bidirectional reviews ─────────────────────

-- v2_client_profiles: one row per unique Client wallet (keyed by HMAC fingerprint).
-- wallet_fingerprint: HMAC-SHA256("naroom:v2:wallet:"+currency+":"+normalized_addr).
-- This is the same fingerprint stored in v2_client_flows.wallet_fingerprint.
-- No raw address, no permanent display name, no flow/invoice/management data.
CREATE TABLE IF NOT EXISTS v2_client_profiles (
    id                 TEXT PRIMARY KEY,
    wallet_fingerprint TEXT NOT NULL UNIQUE,
    currency           TEXT NOT NULL CHECK (currency IN ('BTC', 'LTC')),
    positive_count     INTEGER NOT NULL DEFAULT 0 CHECK (positive_count >= 0),
    negative_count     INTEGER NOT NULL DEFAULT 0 CHECK (negative_count >= 0),
    public_name        TEXT NOT NULL DEFAULT '',
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL,
    -- id: exactly 64 lowercase hex chars
    CHECK (length(id) = 64 AND NOT (id GLOB '*[^0-9a-f]*')),
    -- wallet_fingerprint: exactly 64 lowercase hex chars
    CHECK (length(wallet_fingerprint) = 64 AND NOT (wallet_fingerprint GLOB '*[^0-9a-f]*')),
    CHECK (updated_at >= created_at)
);

-- v2_review_entitlements: one row per purchase × reviewer_side.
-- Created atomically in the same transaction as the contact_ready transition.
-- review_ref: opaque, random. Format: "rev_" + 32 lowercase hex chars (16 random bytes).
-- The Helper website review token is derived server-side from review_ref + HMAC secret;
-- the raw token is never stored. Telegram callback_data encodes the raw_ref only.
CREATE TABLE IF NOT EXISTS v2_review_entitlements (
    id                       TEXT PRIMARY KEY,
    purchase_id              TEXT NOT NULL REFERENCES v2_helper_purchases(id),
    reviewer_side            TEXT NOT NULL CHECK (reviewer_side IN ('client', 'helper')),
    -- Opaque reference stored in DB; the deliverable token is HMAC-derived.
    review_ref               TEXT NOT NULL UNIQUE,
    -- Exactly one target profile set per side (XOR).
    -- client reviewer → target Helper profile
    -- helper reviewer → target Client profile
    target_helper_profile_id TEXT REFERENCES v2_helper_profiles(id),
    target_client_profile_id TEXT REFERENCES v2_client_profiles(id),
    -- expires_at = contact_ready_at + 86400 (enforced at application layer)
    expires_at               INTEGER NOT NULL,
    -- Before consume: both NULL. After consume: both NOT NULL.
    rating                   TEXT CHECK (rating IN ('positive', 'negative')),
    consumed_at              INTEGER,
    created_at               INTEGER NOT NULL,
    updated_at               INTEGER NOT NULL,

    -- One entitlement per purchase per side.
    UNIQUE (purchase_id, reviewer_side),

    -- review_ref format: "rev_" + 32 lowercase hex chars = 36 total
    CHECK (
        length(review_ref) = 36
        AND substr(review_ref, 1, 4) = 'rev_'
        AND NOT (substr(review_ref, 5) GLOB '*[^0-9a-f]*')
    ),

    -- Side-target XOR: client → helper set; helper → client set
    CHECK (
        (reviewer_side = 'client'
            AND target_helper_profile_id IS NOT NULL
            AND target_client_profile_id IS NULL)
        OR
        (reviewer_side = 'helper'
            AND target_client_profile_id IS NOT NULL
            AND target_helper_profile_id IS NULL)
    ),

    -- Atomic consume: both NULL pre-consume, both NOT NULL post-consume
    CHECK ((rating IS NULL) = (consumed_at IS NULL)),

    -- Post-consume timestamp bounds
    CHECK (consumed_at IS NULL OR (consumed_at >= created_at AND consumed_at <= expires_at)),

    -- Exact 24-hour window: createReviewEntitlementsTx uses created_at=contactReadyAt
    -- so expires_at = contactReadyAt + 86400 = created_at + 86400.
    CHECK (expires_at = created_at + 86400),
    CHECK (updated_at >= created_at)
);

-- v2_review_delivery_snapshots: encrypted Client Telegram destination snapshot.
-- Created atomically with the Helper purchase while the Client binding is live.
-- Survives daily binding deletion; deleted after the 24h review window closes.
-- state transitions: awaiting_contact_ready → pending_send → sent | permanent_failure
-- After send or permanent failure: encrypted fields (chat_id_ciphertext, nonce) are NULLed.
CREATE TABLE IF NOT EXISTS v2_review_delivery_snapshots (
    id                   TEXT PRIMARY KEY,
    purchase_id          TEXT NOT NULL UNIQUE REFERENCES v2_helper_purchases(id),
    -- Snapshot of the binding_ref used as AAD at encryption time.
    binding_ref_snapshot TEXT NOT NULL,
    -- AES-GCM encrypted chat_id (hex); NULLed after delivery or permanent failure.
    chat_id_ciphertext   TEXT,
    chat_id_nonce        TEXT,
    key_version          TEXT NOT NULL,
    state                TEXT NOT NULL DEFAULT 'awaiting_contact_ready'
                             CHECK (state IN (
                                 'awaiting_contact_ready', 'pending_send',
                                 'sent', 'permanent_failure'
                             )),
    -- max confirmation horizon: invoice.detection_deadline_at + 86400
    expires_at           INTEGER NOT NULL,
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL,

    -- Encrypted fields present only while state is awaiting_contact_ready or pending_send
    CHECK (
        (state IN ('awaiting_contact_ready', 'pending_send')
            AND chat_id_ciphertext IS NOT NULL
            AND chat_id_nonce IS NOT NULL)
        OR
        (state IN ('sent', 'permanent_failure')
            AND chat_id_ciphertext IS NULL
            AND chat_id_nonce IS NULL)
    ),
    CHECK (length(binding_ref_snapshot) > 0),
    CHECK (length(key_version) > 0),
    CHECK (updated_at >= created_at),
    CHECK (expires_at > created_at)
);

CREATE INDEX IF NOT EXISTS idx_v2_client_profiles_fp    ON v2_client_profiles(wallet_fingerprint);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_v2_client_profiles_public_name ON v2_client_profiles(public_name) WHERE public_name != '';
CREATE INDEX IF NOT EXISTS idx_v2_review_entitlements_purchase ON v2_review_entitlements(purchase_id);
CREATE INDEX IF NOT EXISTS idx_v2_review_entitlements_ref      ON v2_review_entitlements(review_ref);
CREATE INDEX IF NOT EXISTS idx_v2_review_snapshots_purchase    ON v2_review_delivery_snapshots(purchase_id);
CREATE INDEX IF NOT EXISTS idx_v2_review_snapshots_state       ON v2_review_delivery_snapshots(state);

CREATE INDEX IF NOT EXISTS idx_v2_helper_profiles_fp       ON v2_helper_profiles(wallet_fingerprint);
CREATE INDEX IF NOT EXISTS idx_v2_helper_purchases_profile ON v2_helper_purchases(helper_profile_id);
CREATE INDEX IF NOT EXISTS idx_v2_helper_purchases_listing ON v2_helper_purchases(listing_id);
CREATE INDEX IF NOT EXISTS idx_v2_helper_purchases_state   ON v2_helper_purchases(state);
CREATE INDEX IF NOT EXISTS idx_v2_helper_invoices_status   ON v2_helper_invoices(status);
CREATE INDEX IF NOT EXISTS idx_v2_helper_invoices_purchase ON v2_helper_invoices(purchase_id);
-- Partial UNIQUE: at most one non-terminal purchase per (profile, listing).
-- Terminal states (invoice_expired, failed, receipt_expired) are excluded so that
-- a new purchase can be created after the previous one ends.
CREATE UNIQUE INDEX IF NOT EXISTS uniq_v2_helper_purchases_active
ON v2_helper_purchases(helper_profile_id, listing_id)
WHERE state NOT IN ('invoice_expired', 'failed', 'receipt_expired');

-- ── Task 07: Informer V2 ──────────────────────────────────────────────────────
-- Completely isolated from Client/Helper identity. No cross-table FKs to
-- client/helper tables. Raw wallet is never stored; chat_id encrypted at rest.

-- Pending deep-link attempts (15-min TTL). Raw token never stored; only HMAC.
CREATE TABLE IF NOT EXISTS v2_informer_tokens (
    id         TEXT PRIMARY KEY,
    token_hmac TEXT NOT NULL UNIQUE,
    city       TEXT NOT NULL,
    state      TEXT NOT NULL DEFAULT 'pending'
                   CHECK (state IN ('pending', 'claimed', 'expired')),
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_v2_informer_tokens_expires ON v2_informer_tokens(expires_at);

-- One subscription per Telegram chat (one city per chat).
-- sub_ref is an opaque non-secret reference: "isub_" + 32 hex chars.
CREATE TABLE IF NOT EXISTS v2_informer_subscriptions (
    id         TEXT PRIMARY KEY,
    sub_ref    TEXT NOT NULL UNIQUE,
    city       TEXT NOT NULL,
    state      TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'deleted')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_v2_informer_subs_city_state ON v2_informer_subscriptions(city, state);

-- Encrypted chat IDs for Informer subscriptions. Cascade on subscription delete.
CREATE TABLE IF NOT EXISTS v2_informer_destinations (
    sub_ref            TEXT PRIMARY KEY
                           REFERENCES v2_informer_subscriptions(sub_ref) ON DELETE CASCADE,
    chat_id_ciphertext TEXT NOT NULL,
    chat_id_nonce      TEXT NOT NULL,
    key_version        TEXT NOT NULL,
    created_at         INTEGER NOT NULL
);

-- Chat-to-subref lookup via HMAC of chat_id. Cascade on subscription delete.
-- chat_hmac = HMAC-SHA256(hmacKey, "naroom:v2:informer-chat:" + decimal(chatID))
CREATE TABLE IF NOT EXISTS v2_informer_chat_index (
    chat_hmac TEXT PRIMARY KEY,
    sub_ref   TEXT NOT NULL REFERENCES v2_informer_subscriptions(sub_ref) ON DELETE CASCADE
);

-- Outbox: one event per listing (event_key UNIQUE prevents duplicates).
-- listing_id is informational only; no FK to v2_listings (dev fake events must work).
-- event_key = "first_publish:" + listing_id
--
-- Claim ownership (§1):
--   claim_token  — cryptographically random per-claim token (hex-encoded 16 bytes).
--                  Generated fresh on each successful claim; never reused.
--   lease_until  — unix epoch when this claim expires; 0 = unclaimed.
--   claimed_by   — diagnostic worker ID (not used for ownership verification).
--
-- Claim CAS predicate (any mutation): WHERE id=? AND claim_token=? AND lease_until > now
-- Crash recovery: lease_until <= now means the entry is available for re-claim.
CREATE TABLE IF NOT EXISTS v2_informer_outbox (
    id           TEXT PRIMARY KEY,
    listing_id   TEXT NOT NULL,
    city         TEXT NOT NULL,
    display_name TEXT NOT NULL,
    help_type    TEXT NOT NULL,
    dep_type     TEXT NOT NULL,
    urgency      TEXT NOT NULL,
    listing_url  TEXT NOT NULL,
    event_key    TEXT NOT NULL UNIQUE,
    state        TEXT NOT NULL DEFAULT 'pending'
                     CHECK (state IN ('pending', 'done', 'failed')),
    attempt      INTEGER NOT NULL DEFAULT 0,
    claimed_by   TEXT NOT NULL DEFAULT '',
    claim_token  TEXT NOT NULL DEFAULT '',
    lease_until  INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_v2_informer_outbox_state ON v2_informer_outbox(state);
CREATE INDEX IF NOT EXISTS idx_v2_informer_outbox_claim ON v2_informer_outbox(state, lease_until);

-- Per-recipient delivery state for each outbox event (§2).
-- Prevents duplicate delivery to already-notified subscribers on retry.
-- Each recipient has its own attempt counter and terminal state.
--
-- States:
--   pending          — awaiting delivery or retry.
--   delivered        — successfully sent; no further action.
--   permanent_failed — terminal 4xx; subscription deleted.
--   retry_exhausted  — attempts >= MaxRecipientAttempts on retryable error;
--                      subscription NOT deleted (outage ≠ invalid destination).
--   decrypt_failed   — chat_id could not be decrypted; subscription kept.
--
-- Outbox terminal rules:
--   done   — no retry_exhausted rows; all settled (delivered + permanent_failed).
--   failed — at least one retry_exhausted or decrypt_failed row.
--
-- (outbox_id, sub_ref) PRIMARY KEY guarantees exactly-one row per event×recipient.
CREATE TABLE IF NOT EXISTS v2_informer_outbox_recipients (
    outbox_id  TEXT NOT NULL REFERENCES v2_informer_outbox(id) ON DELETE CASCADE,
    sub_ref    TEXT NOT NULL,
    state      TEXT NOT NULL DEFAULT 'pending'
                   CHECK (state IN ('pending', 'delivered', 'permanent_failed',
                                    'retry_exhausted', 'decrypt_failed')),
    attempts   INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (outbox_id, sub_ref)
);
CREATE INDEX IF NOT EXISTS idx_v2_outbox_recipients_pending
    ON v2_informer_outbox_recipients(outbox_id) WHERE state = 'pending';
