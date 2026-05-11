-- Rollback: releases module
DROP INDEX IF EXISTS idx_releases_drafts;
DROP INDEX IF EXISTS idx_releases_published;
DROP TABLE IF EXISTS releases;
