-- Rollback: restore the old single-table model.
-- WARNING: any artifacts added after the up migration will be dropped.
DROP INDEX IF EXISTS idx_release_artifacts_platform;
DROP INDEX IF EXISTS idx_release_artifacts_release;
DROP TABLE IF EXISTS release_artifacts;

DROP INDEX IF EXISTS idx_releases_drafts;
DROP INDEX IF EXISTS idx_releases_published;
DROP TABLE IF EXISTS releases;

ALTER INDEX IF EXISTS idx_releases_legacy_signing_key RENAME TO idx_releases_signing_key;
ALTER INDEX IF EXISTS idx_releases_legacy_drafts RENAME TO idx_releases_drafts;
ALTER INDEX IF EXISTS idx_releases_legacy_published RENAME TO idx_releases_published;
ALTER TABLE releases_legacy RENAME TO releases;
