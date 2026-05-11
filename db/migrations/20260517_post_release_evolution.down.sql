-- Reverse of 20260517_post_release_evolution.up.sql.
-- Sections undone in REVERSE order to respect dependencies (the
-- few there are — these are mostly independent additive changes).

-- 8. refresh_tokens.revoked_at
DROP INDEX IF EXISTS idx_refresh_tokens_revoked;
ALTER TABLE refresh_tokens DROP COLUMN IF EXISTS revoked_at;

-- 7. Seat role 2-tier — widen the CHECK back to 3 values.
-- We can't restore the original 'owner' role data (collapsed to
-- 'admin' in the up). Acceptable since nothing in the codebase
-- distinguished seat-owner from seat-admin behavior.
ALTER TABLE seats DROP CONSTRAINT IF EXISTS seats_role_check;
ALTER TABLE seats ADD CONSTRAINT seats_role_check
    CHECK (role IN ('owner', 'admin', 'member'));

-- 6. Metered Stripe sync
DROP INDEX IF EXISTS idx_metered_billing_identifier;
ALTER TABLE metered_billing
    DROP COLUMN IF EXISTS identifier,
    DROP COLUMN IF EXISTS attempts,
    DROP COLUMN IF EXISTS last_error;
-- Re-introducing the old aggregate UNIQUE on rollback isn't safe if
-- event-log rows exist; leave it off — operators decide.
ALTER TABLE entitlements
    DROP COLUMN IF EXISTS stripe_meter_event_name;

-- 5. Seat invite tokens
DROP INDEX IF EXISTS idx_seats_invite_token_hash;
ALTER TABLE seats
    DROP COLUMN IF EXISTS invite_token_hash,
    DROP COLUMN IF EXISTS invite_expires_at;

-- 4. licenses.past_due_at
DROP INDEX IF EXISTS idx_licenses_past_due_at;
ALTER TABLE licenses DROP COLUMN IF EXISTS past_due_at;

-- 3. seats partial-unique → full-table UNIQUE
-- Re-adding the full-table UNIQUE constraint will fail if any license
-- has both a removed and an active row with the same email. Down is
-- best-effort; operators must clean up first if there's a conflict.
DROP INDEX IF EXISTS idx_seats_unique_active;
ALTER TABLE seats ADD CONSTRAINT seats_license_id_email_key UNIQUE (license_id, email);

-- 2. licenses.external_*
DROP INDEX IF EXISTS idx_licenses_external_workspace;
DROP INDEX IF EXISTS idx_licenses_external_customer;
ALTER TABLE licenses
    DROP COLUMN IF EXISTS external_workspace_id,
    DROP COLUMN IF EXISTS external_customer_id;

-- 1. api_keys.last_used_ip
ALTER TABLE api_keys DROP COLUMN IF EXISTS last_used_ip;
