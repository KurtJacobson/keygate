-- =====================================================
-- Migration: Releases (software distribution / auto-update)
-- Adds the `releases` table for product artifact distribution.
-- Additive only — no DROP on existing data.
-- =====================================================

CREATE TABLE IF NOT EXISTS releases (
    id              TEXT PRIMARY KEY,
    -- ON DELETE RESTRICT: deleting a product must not silently orphan
    -- storage objects. Admins must remove releases first (which triggers
    -- explicit storage cleanup) before deleting the product.
    product_id      TEXT NOT NULL REFERENCES products(id) ON DELETE RESTRICT,

    -- Versioning. version is treated as immutable for a given (product, platform):
    -- once a sha256 is published under v1.2.3, you cannot republish a different
    -- artifact under the same version. Channel is a *property* of the version,
    -- not part of its identity.
    -- The semver regex matches MAJOR.MINOR.PATCH with optional -prerelease and
    -- +build suffixes. service-layer validation is the primary defence; this
    -- CHECK is defence-in-depth for direct SQL writes (data migrations etc.).
    version         TEXT NOT NULL
                    CHECK (length(version) BETWEEN 1 AND 64
                       AND version ~ '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'),
    channel         TEXT NOT NULL DEFAULT 'stable'
                    CHECK (channel IN ('stable','beta','alpha','dev')),

    -- Target. e.g. darwin-arm64, darwin-x64, windows-x64, linux-x64.
    platform        TEXT NOT NULL CHECK (length(platform) BETWEEN 1 AND 64),

    -- Artifact. file_key is the storage object path (R2/S3); URLs are signed on demand.
    file_key        TEXT NOT NULL DEFAULT '' CHECK (length(file_key) <= 1024),
    file_size       BIGINT NOT NULL DEFAULT 0 CHECK (file_size >= 0),
    -- sha256 must be empty (draft, not yet uploaded) or a valid lowercase hex digest.
    sha256          TEXT NOT NULL DEFAULT ''
                    CHECK (sha256 = '' OR sha256 ~ '^[a-f0-9]{64}$'),
    -- ed25519_sig is base64(detached signature of sha256). Reserved for Phase 2 signing.
    ed25519_sig     TEXT NOT NULL DEFAULT '' CHECK (length(ed25519_sig) <= 256),
    content_type    TEXT NOT NULL DEFAULT 'application/octet-stream'
                    CHECK (length(content_type) <= 128),

    -- Display
    name            TEXT NOT NULL DEFAULT '' CHECK (length(name) <= 256),
    -- 64KB caps a pathological release notes from blowing up the table.
    release_notes   TEXT NOT NULL DEFAULT '' CHECK (length(release_notes) <= 65536),

    -- State machine: draft -> published -> yanked (and yanked -> published to undo)
    status          TEXT NOT NULL DEFAULT 'draft'
                    CHECK (status IN ('draft','published','yanked')),
    yanked_reason   TEXT NOT NULL DEFAULT '' CHECK (length(yanked_reason) <= 1024),

    -- Lifecycle timestamps
    published_at    TIMESTAMPTZ,
    yanked_at       TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- One release per (product, version, platform). Channel is a property,
    -- not identity — you cannot have v1.2.3 in both stable and beta with
    -- different sha256, since version is supposed to be an immutable fact.
    UNIQUE(product_id, version, platform)
);

-- Fast feed query: only published rows are interesting for end users.
CREATE INDEX IF NOT EXISTS idx_releases_published
    ON releases(product_id, channel, platform, published_at DESC)
    WHERE status = 'published';

-- Admin-side query: list drafts of a product (small set, partial index).
CREATE INDEX IF NOT EXISTS idx_releases_drafts
    ON releases(product_id, created_at DESC)
    WHERE status = 'draft';
