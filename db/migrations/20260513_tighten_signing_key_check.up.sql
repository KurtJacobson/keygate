-- =====================================================
-- Migration: tighten release_signing_keys.private_key_encrypted CHECK
--
-- The original CHECK accepted [32, 4096] bytes. Our actual format is
-- exactly 60 bytes (12-byte nonce + 32-byte seed + 16-byte GCM tag), so
-- the loose lower bound would let a buggy producer write 32-byte garbage
-- (e.g. a seed without nonce) past the constraint, only failing at decrypt
-- with an opaque "decryption failed" error.
--
-- We tighten the upper bound modestly (256) to allow some future flex,
-- and raise the lower bound to 60 — the current scheme's exact size.
-- =====================================================

ALTER TABLE release_signing_keys
    DROP CONSTRAINT IF EXISTS release_signing_keys_private_key_encrypted_check;

ALTER TABLE release_signing_keys
    ADD CONSTRAINT release_signing_keys_private_key_encrypted_check
        CHECK (octet_length(private_key_encrypted) BETWEEN 60 AND 256);
