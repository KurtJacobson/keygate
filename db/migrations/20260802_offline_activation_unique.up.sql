-- =====================================================
-- Migration: one offline activation per license
--
-- Self-service offline activations are marked identifier_type='offline'.
-- A customer may self-activate exactly one offline (air-gapped) machine;
-- resetting it requires an admin to clear the activation. Enforce the
-- "one offline machine" rule atomically at the DB layer with a partial
-- unique index, so concurrent requests can't race past the cap.
-- =====================================================

-- Allow the new 'offline' identifier_type (self-service air-gapped
-- activations) alongside the existing device/user values.
ALTER TABLE activations DROP CONSTRAINT IF EXISTS activations_identifier_type_check;
ALTER TABLE activations ADD CONSTRAINT activations_identifier_type_check
    CHECK (identifier_type IN ('device', 'user', 'offline'));

CREATE UNIQUE INDEX IF NOT EXISTS activations_one_offline_per_license
    ON activations (license_id)
    WHERE identifier_type = 'offline';
