DROP INDEX IF EXISTS idx_releases_signing_key;
ALTER TABLE releases DROP COLUMN IF EXISTS signing_key_id;
