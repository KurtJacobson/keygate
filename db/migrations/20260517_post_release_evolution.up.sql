-- =====================================================
-- Migration: Post-release schema evolution (consolidated)
--
-- Bundles eight additive schema changes that originally landed as
-- separate migrations (20260517 … 20260524). All independent ALTERs;
-- no inter-dependencies. Merged for a tidier on-disk story now that
-- we know none of them needed to ship separately.
--
-- Sections:
--   1. api_keys.last_used_ip          (trace leaked credentials)
--   2. licenses.external_*            (merchant-owned ids)
--   3. seats partial-unique index     (re-add after soft-delete)
--   4. licenses.past_due_at           (dunning clock anchor)
--   5. seats invite-token columns     (token-claim flow)
--   6. metered_billing event-log shape + entitlements.meter_event_name
--   7. seat role 2-tier               (collapse 'owner' → 'admin')
--   8. refresh_tokens.revoked_at      (rotation reuse detection)
-- =====================================================


-- ─── 1. api_keys.last_used_ip ──────────────────────────────
-- Track the source IP of the most recent successful API key auth.
-- Pairs with the existing api_keys.last_used column so the admin UI
-- can show "last seen from <ip> at <time>". Helps spot leaked keys
-- (unexpected geo / cloud range / dev laptop showing up in prod).
ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS last_used_ip TEXT NOT NULL DEFAULT '';


-- ─── 2. licenses.external_customer_id / external_workspace_id ──
-- External identifiers let merchants map their own user/workspace
-- model onto Keygate licenses without an intermediate mapping table.
-- Both are opaque strings — Keygate doesn't interpret the contents.
-- Indexes are partial (empty-string excluded) so the common "no
-- external id" case doesn't bloat the index.
ALTER TABLE licenses
    ADD COLUMN IF NOT EXISTS external_customer_id  TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS external_workspace_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_licenses_external_customer
    ON licenses(product_id, external_customer_id)
    WHERE external_customer_id <> '';

CREATE INDEX IF NOT EXISTS idx_licenses_external_workspace
    ON licenses(product_id, external_workspace_id)
    WHERE external_workspace_id <> '';


-- ─── 3. seats partial-unique on ACTIVE rows ────────────────
-- Original UNIQUE(license_id, email) treated soft-deleted rows like
-- live ones; re-adding the same email after a remove silently broke
-- with a 500. Partial unique (WHERE removed_at IS NULL) lets re-add
-- succeed while the removed row stays for audit history.
ALTER TABLE seats DROP CONSTRAINT IF EXISTS seats_license_id_email_key;

CREATE UNIQUE INDEX IF NOT EXISTS idx_seats_unique_active
    ON seats (license_id, email)
    WHERE removed_at IS NULL;


-- ─── 4. licenses.past_due_at (dunning anchor) ──────────────
-- Original code derived "days past due" from licenses.updated_at,
-- which bumps on EVERY UPDATE (sub-state sync, audit, admin notes).
-- Each bump silently reset the dunning ladder back to day 0, so the
-- day-7 / day-14 reminders never fired in many cases.
-- past_due_at is set by the payment-failed handler and cleared when
-- the license leaves past_due. The dunning ladder reads only this.
ALTER TABLE licenses
    ADD COLUMN IF NOT EXISTS past_due_at TIMESTAMPTZ;

-- Backfill: best-effort starting point for rows already in past_due.
UPDATE licenses
   SET past_due_at = updated_at
 WHERE status = 'past_due'
   AND past_due_at IS NULL;

-- Partial index keeps ExpiryChecker scans cheap.
CREATE INDEX IF NOT EXISTS idx_licenses_past_due_at
    ON licenses (past_due_at)
    WHERE status = 'past_due';


-- ─── 5. seats invite-token columns ─────────────────────────
-- Token-based seat acceptance flow:
--   invite_token_hash: SHA256(plain_token), unique among live invites
--   invite_expires_at: 7 days by default
-- The plain token is emailed inside the claim URL and never stored.
ALTER TABLE seats
    ADD COLUMN IF NOT EXISTS invite_token_hash TEXT,
    ADD COLUMN IF NOT EXISTS invite_expires_at TIMESTAMPTZ;

CREATE UNIQUE INDEX IF NOT EXISTS idx_seats_invite_token_hash
    ON seats (invite_token_hash)
    WHERE invite_token_hash IS NOT NULL;


-- ─── 6. Stripe Billing Meter sync ──────────────────────────
-- Stripe v82 SDK exposes only the Billing Meter API. Each event
-- carries event_name (which meter), customer, value, and an
-- idempotency identifier. Three additive changes:
--   - entitlements.stripe_meter_event_name: maps Keygate feature to
--     the Stripe meter the merchant configured.
--   - metered_billing event-log shape: identifier column + drop old
--     aggregate UNIQUE (now one row per RecordUsage).
--   - attempts + last_error so operators can spot stuck rows from
--     the admin UI without log-diving.
ALTER TABLE entitlements
    ADD COLUMN IF NOT EXISTS stripe_meter_event_name TEXT NOT NULL DEFAULT '';

ALTER TABLE metered_billing
    ADD COLUMN IF NOT EXISTS identifier TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS attempts   INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_error TEXT NOT NULL DEFAULT '';

-- The aggregate-overwrite constraint doesn't fit a delta-based meter.
ALTER TABLE metered_billing
    DROP CONSTRAINT IF EXISTS metered_billing_license_id_feature_period_key_key;

-- Partial-unique because legacy rows have identifier=''.
CREATE UNIQUE INDEX IF NOT EXISTS idx_metered_billing_identifier
    ON metered_billing(identifier)
    WHERE identifier <> '';


-- ─── 7. Seat role 2-tier (admin / member only) ─────────────
-- License owner is implicit via license.email and never has a seat
-- row of their own, so seat.role='owner' was a redundant + UI-
-- invisible state. internal/service/seats.go AddSeat enforces the
-- 2-tier rule at write time; this CHECK is defence-in-depth.
UPDATE seats SET role = 'admin' WHERE role = 'owner';

ALTER TABLE seats DROP CONSTRAINT IF EXISTS seats_role_check;
ALTER TABLE seats ADD CONSTRAINT seats_role_check
    CHECK (role IN ('admin', 'member'));


-- ─── 8. refresh_tokens.revoked_at (reuse detection) ────────
-- DELETE-on-rotate let an attacker who captured an old (already-
-- rotated) token try once and get a clean 401 — indistinguishable
-- from a stale token. revoked_at lets the rotation path detect a
-- replay and revoke EVERY refresh_token for the user.
-- See internal/handler/oauth.go Refresh + store.RotateRefreshToken.
-- RFC 6819 §5.2.2.3 / OAuth2 best practice.
ALTER TABLE refresh_tokens
    ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ NULL;

CREATE INDEX IF NOT EXISTS idx_refresh_tokens_revoked
    ON refresh_tokens(revoked_at)
    WHERE revoked_at IS NOT NULL;
