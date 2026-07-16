-- =====================================================
-- Migration: Support window (perpetual license + paid support)
--
-- Adds the "perpetual license with paid support" model: the license
-- itself never expires, but updates/support are gated by a separate
-- support window (JetBrains-style perpetual fallback — customers
-- keep every version released while their support was active).
--
--   1. licenses.support_until  NULL = unlimited support (backwards
--      compatible: existing licenses keep full update access)
--   2. plans.support_days      default support window for newly
--      issued licenses; 0 = no default (unlimited)
-- =====================================================

ALTER TABLE licenses
    ADD COLUMN IF NOT EXISTS support_until TIMESTAMPTZ;

ALTER TABLE plans
    ADD COLUMN IF NOT EXISTS support_days INTEGER NOT NULL DEFAULT 0;
