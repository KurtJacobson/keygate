-- =====================================================
-- Migration: Support-renewal Stripe price
--
-- Per-plan Stripe price used to sell a support-window renewal (a
-- one-time charge that extends licenses.support_until). Empty means
-- self-serve support renewal is not offered for the plan.
-- =====================================================

ALTER TABLE plans
    ADD COLUMN IF NOT EXISTS support_renewal_price_id TEXT NOT NULL DEFAULT '';
