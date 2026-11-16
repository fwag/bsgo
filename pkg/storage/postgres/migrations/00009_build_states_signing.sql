-- +goose Up
-- signing: the job built, its RPMs are being signed (OBS "S").
-- finished: signed, waiting for integration into the build repository (OBS "f").
-- dispatching: reserved for the dispatcher handing a scheduled job to a worker.
ALTER TABLE package_build_state DROP CONSTRAINT IF EXISTS package_build_state_state_check;
ALTER TABLE package_build_state ADD CONSTRAINT package_build_state_state_check CHECK (state IN (
    'scheduled', 'dispatching', 'building', 'signing', 'finished', 'succeeded', 'failed',
    'unresolvable', 'blocked', 'broken', 'excluded', 'disabled'));

-- +goose Down
UPDATE package_build_state SET state = 'scheduled' WHERE state = 'dispatching';
UPDATE package_build_state SET state = 'building' WHERE state IN ('signing', 'finished');
ALTER TABLE package_build_state DROP CONSTRAINT IF EXISTS package_build_state_state_check;
ALTER TABLE package_build_state ADD CONSTRAINT package_build_state_state_check CHECK (state IN (
    'scheduled', 'building', 'succeeded', 'failed', 'unresolvable',
    'blocked', 'broken', 'excluded', 'disabled'));
