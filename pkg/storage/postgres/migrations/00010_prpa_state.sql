-- +goose Up
-- Repository state of one (project, repo, arch), after the OBS repository
-- states: broken, building, blocked, finished, publishing, published,
-- unpublished. index_hash ties published to the build repository index.
CREATE TABLE IF NOT EXISTS prpa_state (
    project    VARCHAR(128) NOT NULL,
    repo       VARCHAR(128) NOT NULL,
    arch       VARCHAR(64)  NOT NULL,
    state      VARCHAR(32)  NOT NULL CHECK (state IN (
        'broken', 'building', 'blocked', 'finished', 'publishing', 'published', 'unpublished')),
    details    TEXT         NOT NULL DEFAULT '',
    index_hash VARCHAR(128) NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project, repo, arch)
);

-- +goose Down
DROP TABLE IF EXISTS prpa_state;
