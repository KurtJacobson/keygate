-- =====================================================
-- Migration: post-bundle evolution (consolidated)
--
-- Squashes 20260516..20260521 from the previous design iteration. Each
-- was a small additive change, plus one cancel-out pair
-- (release_feed_tokens created then dropped — that idea was scrapped
-- after we realised gating non-stable feeds with a separate token type
-- duplicated api_keys' job).
--
-- Pre-launch — no production data depends on the granular history, so
-- this rolls forward as one logical unit:
--
--   1. products.minimum_supported_version + minimum_supported_message
--      Per-product floor for the auto-update feed; clients below this
--      get a forced-upgrade prompt.
--
--   2. products.require_signing (DEFAULT TRUE — secure default)
--      Publishing a release with no active signing key fails 409
--      unless the operator explicitly disables this. Stops the silent
--      "shipped unsigned, updater silently rejects" failure mode.
--
--   3. idempotency_keys table
--      Cache for Idempotency-Key replays on /license/activate etc.
--      24h TTL via expires_at + a background pruner.
--
--   4. api_keys.product_id → NULLABLE
--      System-wide admin api_keys (CI/CD, cross-product migrations)
--      have NULL product_id + the `admin` scope. Per-product keys
--      still set product_id explicitly.
--
-- All DDL uses IF NOT EXISTS so this is fully idempotent: running on
-- a dev DB that already applied the 6 original migrations is a no-op;
-- running on a fresh DB produces exactly the target schema.
-- =====================================================

-- 1+2. New product columns.
ALTER TABLE products
    ADD COLUMN IF NOT EXISTS minimum_supported_version TEXT NOT NULL DEFAULT ''
        CHECK (
            minimum_supported_version = ''
            OR minimum_supported_version ~ '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'
        ),
    ADD COLUMN IF NOT EXISTS minimum_supported_message TEXT NOT NULL DEFAULT ''
        CHECK (length(minimum_supported_message) <= 1024),
    ADD COLUMN IF NOT EXISTS require_signing BOOLEAN NOT NULL DEFAULT TRUE;

-- 3. idempotency_keys.
CREATE TABLE IF NOT EXISTS idempotency_keys (
    key               TEXT    NOT NULL CHECK (length(key) BETWEEN 1 AND 256),
    endpoint          TEXT    NOT NULL CHECK (length(endpoint) BETWEEN 1 AND 256),
    body_hash         TEXT    NOT NULL CHECK (body_hash ~ '^[a-f0-9]{64}$'),
    response_status   INT     NOT NULL DEFAULT 0,
    response_body     TEXT    NOT NULL DEFAULT '',
    response_complete BOOLEAN NOT NULL DEFAULT FALSE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- 24h TTL — Stripe's standard. Cleanup runs on a periodic timer.
    expires_at        TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '24 hours'),
    PRIMARY KEY (key, endpoint)
);

CREATE INDEX IF NOT EXISTS idx_idempotency_keys_expires_at
    ON idempotency_keys(expires_at);

-- 4. api_keys.product_id → nullable. DROP NOT NULL is idempotent in
-- PostgreSQL (no-op if the column is already nullable).
ALTER TABLE api_keys
    ALTER COLUMN product_id DROP NOT NULL;
