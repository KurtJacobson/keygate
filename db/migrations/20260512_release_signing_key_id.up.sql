-- =====================================================
-- Migration: Bind release rows to the signing key that produced their sig.
--
-- Without this column, a published release with ed25519_sig set has no way
-- to know which key produced it — and after key rotation, clients with the
-- old public key embedded would silently fail verification.
--
-- ON DELETE SET NULL: if a key row is somehow deleted, the release loses
-- its sig binding but stays valid (the sig is still on disk and will fail
-- verification at the client, which is the correct behaviour).
-- =====================================================

ALTER TABLE releases
    ADD COLUMN IF NOT EXISTS signing_key_id TEXT REFERENCES release_signing_keys(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_releases_signing_key
    ON releases(signing_key_id)
    WHERE signing_key_id IS NOT NULL;
