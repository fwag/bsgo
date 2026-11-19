-- +goose Up
-- The structured cause of a blocked row (producing source packages with their
-- state, unfinished cycles); details keeps the OBS-style summary. NULL for
-- every other state and for rows written before this column existed.
ALTER TABLE package_build_state ADD COLUMN IF NOT EXISTS blocked JSONB;

-- +goose Down
ALTER TABLE package_build_state DROP COLUMN IF EXISTS blocked;
