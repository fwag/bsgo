-- +goose Up
CREATE TABLE IF NOT EXISTS target_solv_cache (
    project_name VARCHAR(128) NOT NULL,
    target_name  VARCHAR(128) NOT NULL,
    arch         VARCHAR(64) NOT NULL,
    solv_blob    BYTEA NOT NULL,
    content_hash VARCHAR(64) NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project_name, target_name, arch)
);

CREATE INDEX IF NOT EXISTS idx_target_solv_cache_proj_tgt
    ON target_solv_cache(project_name, target_name);

-- +goose Down
DROP TABLE IF EXISTS target_solv_cache;
