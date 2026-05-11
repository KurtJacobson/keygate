-- =====================================================
-- Migration: Release-as-bundle refactor
--
-- Industry standard (GitHub Releases / Keygen / npm):
--   - A "release" is a logical event tagged with a version.
--   - A release contains zero or more platform-specific "artifacts".
--   - Lifecycle (draft / published / yanked) is on the release.
--   - sha256 / ed25519_sig live on the artifact (per-platform binary).
--
-- Old schema treated each (product, version, platform) as its own
-- release row, conflating two concepts. This migration:
--   1. Renames the existing `releases` to `releases_legacy` (rollback evidence)
--   2. Creates new `releases` table (logical, no per-platform fields)
--   3. Creates `release_artifacts` table (per-platform binary metadata)
--   4. Migrates data: deduplicate (product_id, version) into release rows,
--      original rows become artifact rows linked to the new release.
--
-- Rollback: drop new tables, rename releases_legacy back to releases.
-- =====================================================

-- 0. Precondition: legacy schema's UNIQUE(product_id, version, platform)
--    permits the same (product, version, platform) tuple to exist across
--    multiple channels (channel was not part of the unique key). Such rows
--    would collapse into duplicate (release_id, platform) artifact rows
--    and violate the new UNIQUE(release_id, platform). Refuse to migrate
--    if dirty data is present — operator must reconcile manually first.
DO $$ BEGIN
    IF EXISTS (
        SELECT 1 FROM releases
        GROUP BY product_id, version, platform
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION
            'legacy releases table has duplicate (product_id, version, platform) tuples across channels — manual reconciliation required before this migration can run';
    END IF;
END $$;

-- 1. Rename old table to keep data + indices intact for rollback.
ALTER TABLE releases RENAME TO releases_legacy;
ALTER INDEX IF EXISTS idx_releases_published RENAME TO idx_releases_legacy_published;
ALTER INDEX IF EXISTS idx_releases_drafts RENAME TO idx_releases_legacy_drafts;
ALTER INDEX IF EXISTS idx_releases_signing_key RENAME TO idx_releases_legacy_signing_key;

-- 2. New `releases` table — logical release.
CREATE TABLE releases (
    id              TEXT PRIMARY KEY,
    product_id      TEXT NOT NULL REFERENCES products(id) ON DELETE RESTRICT,

    -- Version is unique per product (NOT per platform). Cannot publish
    -- v1.2.3 twice — but a single v1.2.3 release can carry multiple
    -- platform artifacts.
    version         TEXT NOT NULL
                    CHECK (length(version) BETWEEN 1 AND 64
                       AND version ~ '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'),
    channel         TEXT NOT NULL DEFAULT 'stable'
                    CHECK (channel IN ('stable','beta','alpha','dev')),

    -- Display
    name            TEXT NOT NULL DEFAULT '' CHECK (length(name) <= 256),
    release_notes   TEXT NOT NULL DEFAULT '' CHECK (length(release_notes) <= 65536),

    -- Lifecycle: applies to the whole release including all artifacts.
    status          TEXT NOT NULL DEFAULT 'draft'
                    CHECK (status IN ('draft','published','yanked')),
    yanked_reason   TEXT NOT NULL DEFAULT '' CHECK (length(yanked_reason) <= 1024),

    published_at    TIMESTAMPTZ,
    yanked_at       TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE(product_id, version)
);

CREATE INDEX idx_releases_published
    ON releases(product_id, channel, published_at DESC)
    WHERE status = 'published';

CREATE INDEX idx_releases_drafts
    ON releases(product_id, created_at DESC)
    WHERE status = 'draft';

-- 3. New `release_artifacts` table — per-platform binary.
CREATE TABLE release_artifacts (
    id              TEXT PRIMARY KEY,
    release_id      TEXT NOT NULL REFERENCES releases(id) ON DELETE CASCADE,

    platform        TEXT NOT NULL CHECK (length(platform) BETWEEN 1 AND 64),

    -- Storage
    file_key        TEXT NOT NULL DEFAULT '' CHECK (length(file_key) <= 1024),
    file_size       BIGINT NOT NULL DEFAULT 0 CHECK (file_size >= 0),
    sha256          TEXT NOT NULL DEFAULT ''
                    CHECK (sha256 = '' OR sha256 ~ '^[a-f0-9]{64}$'),
    ed25519_sig     TEXT NOT NULL DEFAULT '' CHECK (length(ed25519_sig) <= 256),
    content_type    TEXT NOT NULL DEFAULT 'application/octet-stream'
                    CHECK (length(content_type) <= 128),

    -- Which signing key produced ed25519_sig (nullable for unsigned artifacts).
    signing_key_id  TEXT REFERENCES release_signing_keys(id) ON DELETE SET NULL,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- One artifact per platform per release.
    UNIQUE(release_id, platform)
);

CREATE INDEX idx_release_artifacts_release
    ON release_artifacts(release_id);

CREATE INDEX idx_release_artifacts_platform
    ON release_artifacts(platform);

-- 4. Data migration: existing legacy rows → new schema.
-- Each legacy row is BOTH a release event AND an artifact. We deduplicate
-- on (product_id, version) — picking the most "advanced" status (yanked >
-- published > draft) and the most relevant channel/notes.
--
-- For our current fleet (5 rows, all distinct versions) this is a
-- straightforward 1:1 → split.

-- 4a. Insert one release row per distinct (product_id, version).
INSERT INTO releases (
    id, product_id, version, channel, name, release_notes,
    status, yanked_reason, published_at, yanked_at, created_at, updated_at
)
SELECT
    -- Use the legacy ID for the release if there's only one row per
    -- (product, version); otherwise pick the first ID by created_at.
    -- We rely on the COUNT == DISTINCT precondition for production data,
    -- which we verified before this migration.
    (SELECT l.id FROM releases_legacy l
        WHERE l.product_id = legacy.product_id AND l.version = legacy.version
        ORDER BY l.created_at ASC LIMIT 1) as id,
    legacy.product_id,
    legacy.version,
    -- Channel: pick the most-stable channel present (matches the visible-to-most-users semantic).
    -- Order: stable > beta > alpha > dev.
    (SELECT l.channel FROM releases_legacy l
        WHERE l.product_id = legacy.product_id AND l.version = legacy.version
        ORDER BY CASE l.channel WHEN 'stable' THEN 1 WHEN 'beta' THEN 2 WHEN 'alpha' THEN 3 ELSE 4 END
        LIMIT 1) as channel,
    -- Name + notes: pick the longest non-empty (heuristic).
    COALESCE((SELECT l.name FROM releases_legacy l
        WHERE l.product_id = legacy.product_id AND l.version = legacy.version
        AND l.name <> '' ORDER BY length(l.name) DESC LIMIT 1), '') as name,
    COALESCE((SELECT l.release_notes FROM releases_legacy l
        WHERE l.product_id = legacy.product_id AND l.version = legacy.version
        AND l.release_notes <> '' ORDER BY length(l.release_notes) DESC LIMIT 1), '') as release_notes,
    -- Status: collapse to the most-advanced state across rows.
    -- yanked > published > draft. If any row was yanked, the whole release inherits.
    (SELECT l.status FROM releases_legacy l
        WHERE l.product_id = legacy.product_id AND l.version = legacy.version
        ORDER BY CASE l.status WHEN 'yanked' THEN 1 WHEN 'published' THEN 2 ELSE 3 END
        LIMIT 1) as status,
    COALESCE((SELECT l.yanked_reason FROM releases_legacy l
        WHERE l.product_id = legacy.product_id AND l.version = legacy.version
        AND l.yanked_reason <> '' LIMIT 1), '') as yanked_reason,
    (SELECT MIN(l.published_at) FROM releases_legacy l
        WHERE l.product_id = legacy.product_id AND l.version = legacy.version
        AND l.published_at IS NOT NULL) as published_at,
    (SELECT MAX(l.yanked_at) FROM releases_legacy l
        WHERE l.product_id = legacy.product_id AND l.version = legacy.version
        AND l.yanked_at IS NOT NULL) as yanked_at,
    MIN(legacy.created_at) as created_at,
    MAX(legacy.updated_at) as updated_at
FROM releases_legacy legacy
GROUP BY legacy.product_id, legacy.version;

-- 4b. Insert one artifact row per legacy row, linked to the release just created.
INSERT INTO release_artifacts (
    id, release_id, platform, file_key, file_size, sha256,
    ed25519_sig, content_type, signing_key_id, created_at, updated_at
)
SELECT
    -- Generate a fresh UUID-like ID for each artifact. Postgres-side via
    -- gen_random_uuid()::text — pgcrypto extension is in default 18.
    md5(legacy.id || ':' || legacy.platform) as id,
    r.id as release_id,
    legacy.platform,
    legacy.file_key,
    legacy.file_size,
    legacy.sha256,
    legacy.ed25519_sig,
    legacy.content_type,
    legacy.signing_key_id,
    legacy.created_at,
    legacy.updated_at
FROM releases_legacy legacy
JOIN releases r ON r.product_id = legacy.product_id AND r.version = legacy.version;
