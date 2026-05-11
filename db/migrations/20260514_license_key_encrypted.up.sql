-- =====================================================
-- Migration: License key encryption at rest (Phase A).
--
-- Adds a column to store the AES-256-GCM-encrypted license key alongside
-- the existing plaintext column. New license inserts populate both
-- columns; reads prefer the encrypted column with a fallback to plaintext
-- for un-migrated rows.
--
-- Future Phase B will backfill existing rows; Phase C will drop the
-- plaintext column. We keep the dual-write for now so a rollback is safe.
--
-- Encryption details:
--   - master key = HKDF-SHA256(RELEASE_KEY_ENCRYPTION_KEY, "keygate-v1-license-key")
--   - aad = license.id (UUID, present at insert time)
--   - layout = nonce(12) || ciphertext || tag(16); typical 26-byte license keys
--     produce ~54 byte ciphertexts. The 4096 ceiling allows future-proofing.
-- =====================================================

ALTER TABLE licenses
    ADD COLUMN IF NOT EXISTS license_key_encrypted BYTEA NULL
        CHECK (license_key_encrypted IS NULL OR octet_length(license_key_encrypted) BETWEEN 28 AND 4096);
