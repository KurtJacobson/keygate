-- Loosen back to the original bounds.
ALTER TABLE release_signing_keys
    DROP CONSTRAINT IF EXISTS release_signing_keys_private_key_encrypted_check;

ALTER TABLE release_signing_keys
    ADD CONSTRAINT release_signing_keys_private_key_encrypted_check
        CHECK (octet_length(private_key_encrypted) BETWEEN 32 AND 4096);
