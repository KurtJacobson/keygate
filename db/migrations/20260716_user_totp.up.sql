-- Two-factor auth (TOTP) columns.
--   totp_secret    base32 shared secret; set at setup time, NULL when 2FA is off
--   totp_enabled   FALSE while enrollment is pending (secret issued but not
--                  yet confirmed with a valid code), TRUE once active
--   totp_last_slot last accepted 30s time-slot; codes at or before this
--                  slot are rejected so a captured code can't be replayed
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_secret TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_enabled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_last_slot BIGINT NOT NULL DEFAULT 0;
