-- +goose Up
CREATE TABLE IF NOT EXISTS package_build_state (
    project    VARCHAR(128) NOT NULL,
    repo       VARCHAR(128) NOT NULL,
    arch       VARCHAR(64)  NOT NULL,
    package    VARCHAR(256) NOT NULL,
    flavor     VARCHAR(128) NOT NULL DEFAULT '',
    state      VARCHAR(32)  NOT NULL,
    details    TEXT         NOT NULL DEFAULT '',
    source_commit VARCHAR(64) NOT NULL DEFAULT '', -- git object id (SHA-1 or SHA-256)
    meta       VARCHAR(64)  NOT NULL DEFAULT '', -- sha256(source_commit + sorted bdep sha256)
    bcnt       INTEGER      NOT NULL DEFAULT 0,
    job_id     VARCHAR(128) NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project, repo, arch, package, flavor),
    CONSTRAINT package_build_state_state_check CHECK (state IN (
        'scheduled', 'building', 'succeeded', 'failed', 'unresolvable',
        'blocked', 'broken', 'excluded', 'disabled'))
);

CREATE INDEX IF NOT EXISTS idx_package_build_state_proj_state
    ON package_build_state(project, state);

-- +goose Down
DROP TABLE IF EXISTS package_build_state;
