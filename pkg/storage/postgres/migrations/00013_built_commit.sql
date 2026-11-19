-- +goose Up
-- The source commit of the last succeeded build, kept like built_meta:
-- source_commit moves on as soon as a new build is scheduled. Only succeeded
-- rows can be backfilled: in every other state source_commit may already be
-- the commit of a build that has not produced the stored binaries, and the
-- commit of those is unknown. They stay empty and count by checksum until the
-- next successful build.
ALTER TABLE package_build_state ADD COLUMN IF NOT EXISTS built_commit VARCHAR(64) NOT NULL DEFAULT '';
UPDATE package_build_state SET built_commit = source_commit
    WHERE built_meta <> '' AND built_commit = '' AND state = 'succeeded';

-- +goose Down
ALTER TABLE package_build_state DROP COLUMN IF EXISTS built_commit;
