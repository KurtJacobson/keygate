-- =====================================================
-- Migration: Release artifact signing keys
-- Per-product Ed25519 keypair for signing release artifacts.
-- Private key is AES-256-GCM encrypted at rest using the master
-- key from RELEASE_KEY_ENCRYPTION_KEY.
-- Additive only — no DROP on existing data.
-- =====================================================

CREATE TABLE IF NOT EXISTS release_signing_keys (
    id                    TEXT PRIMARY KEY,
    -- Cascade: deleting a product removes its keys (the products table
    -- already RESTRICTs deletion if releases exist, so cascade is safe here).
    product_id            TEXT NOT NULL REFERENCES products(id) ON DELETE CASCADE,

    -- Ed25519 public key, base64-encoded (44 chars padded). Distributed to
    -- clients (Sparkle, Tauri, Velopack) for verifying release artifacts.
    public_key            TEXT NOT NULL
                          CHECK (length(public_key) BETWEEN 32 AND 128),

    -- Private key encrypted under the master key.
    -- Layout: nonce (12 bytes) || ciphertext (ed25519 private key seed = 32 bytes
    -- with AES-GCM tag = 16 bytes). Total exact 60 bytes.
    -- Stored as bytea, never exposed via API.
    private_key_encrypted BYTEA NOT NULL
                          CHECK (octet_length(private_key_encrypted) BETWEEN 32 AND 4096),

    -- One active key per product at a time. Rotation deactivates the old key
    -- (active=false) and inserts a new one (active=true) inside a transaction.
    active                BOOLEAN NOT NULL DEFAULT TRUE,

    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_at            TIMESTAMPTZ,

    -- Optional human-friendly label (e.g. "rotated after key compromise 2026-09")
    note                  TEXT NOT NULL DEFAULT ''
                          CHECK (length(note) <= 256)
);

-- Enforce "at most one active key per product" via partial unique index.
CREATE UNIQUE INDEX IF NOT EXISTS idx_release_signing_keys_one_active
    ON release_signing_keys(product_id)
    WHERE active = TRUE;

-- Lookup: history list per product (active first, then by creation time desc).
CREATE INDEX IF NOT EXISTS idx_release_signing_keys_product
    ON release_signing_keys(product_id, active, created_at DESC);
