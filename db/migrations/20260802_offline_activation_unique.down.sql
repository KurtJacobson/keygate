DROP INDEX IF EXISTS activations_one_offline_per_license;

-- Revert the identifier_type check to its original device/user set.
-- (Fails if any 'offline' activations still exist — clear them first.)
ALTER TABLE activations DROP CONSTRAINT IF EXISTS activations_identifier_type_check;
ALTER TABLE activations ADD CONSTRAINT activations_identifier_type_check
    CHECK (identifier_type IN ('device', 'user'));
