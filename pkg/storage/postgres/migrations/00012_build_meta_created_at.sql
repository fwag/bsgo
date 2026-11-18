-- +goose Up
-- Unreferenced metas are only pruned after a grace period: a superseded job
-- may still finish and then refers to its meta.
ALTER TABLE build_meta ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

-- +goose Down
ALTER TABLE build_meta DROP COLUMN IF EXISTS created_at;
