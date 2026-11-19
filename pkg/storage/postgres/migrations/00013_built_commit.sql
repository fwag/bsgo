-- +goose Up
-- The source commit of the last succeeded build, kept like built_meta:
-- source_commit moves on as soon as a new build is scheduled. Builds from
-- before the column existed are assumed to be of the stored source commit.
ALTER TABLE package_build_state ADD COLUMN IF NOT EXISTS built_commit VARCHAR(64) NOT NULL DEFAULT '';
UPDATE package_build_state SET built_commit = source_commit WHERE built_meta <> '' AND built_commit = '';

-- +goose Down
ALTER TABLE package_build_state DROP COLUMN IF EXISTS built_commit;
