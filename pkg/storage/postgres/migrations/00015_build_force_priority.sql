-- +goose Up
-- The trigger tier ('high', 'med', 'low') of a forced rebuild request. It
-- boosts the dispatch priority of the job the request produces; '' is low.
ALTER TABLE build_force ADD COLUMN IF NOT EXISTS priority VARCHAR(8) NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE build_force DROP COLUMN IF EXISTS priority;
