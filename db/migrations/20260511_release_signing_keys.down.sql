-- Rollback: release_signing_keys
DROP INDEX IF EXISTS idx_release_signing_keys_product;
DROP INDEX IF EXISTS idx_release_signing_keys_one_active;
DROP TABLE IF EXISTS release_signing_keys;
