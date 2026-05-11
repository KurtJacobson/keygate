-- Rollback for the consolidated 20260516 migration.
--
-- WARNING: this discards in-flight idempotency state AND removes any
-- system-wide admin api_keys (those have product_id IS NULL and would
-- violate the restored NOT NULL constraint).

-- 4. Restore api_keys.product_id NOT NULL.
DELETE FROM api_keys WHERE product_id IS NULL;
ALTER TABLE api_keys ALTER COLUMN product_id SET NOT NULL;

-- 3. Drop idempotency_keys.
DROP INDEX IF EXISTS idx_idempotency_keys_expires_at;
DROP TABLE IF EXISTS idempotency_keys;

-- 1+2. Drop product columns.
ALTER TABLE products
    DROP COLUMN IF EXISTS require_signing,
    DROP COLUMN IF EXISTS minimum_supported_message,
    DROP COLUMN IF EXISTS minimum_supported_version;
