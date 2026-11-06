package postgres

const InitialSchema = `
CREATE TABLE IF NOT EXISTS projects (
    name            VARCHAR(128) PRIMARY KEY,
    title           VARCHAR(255),
    description     TEXT,
    manifest_yaml   TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS project_packages (
    project_name    VARCHAR(128) NOT NULL REFERENCES projects(name) ON DELETE CASCADE,
    package_name    VARCHAR(128) NOT NULL,
    shard           VARCHAR(64) NOT NULL,
    git_url         VARCHAR(512) NOT NULL,
    branch          VARCHAR(128),
    commit_sha      VARCHAR(64),
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    memory_limit    VARCHAR(32),
    vcpus           INT,
    disk_limit      VARCHAR(32),
    timeout         VARCHAR(32),
    manifest_yaml   TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project_name, package_name)
);

CREATE INDEX IF NOT EXISTS idx_pkg_shard ON project_packages(shard);
`
