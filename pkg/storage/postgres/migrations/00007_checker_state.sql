-- +goose Up
-- built_meta is the meta of the last succeeded build; meta is the meta of the
-- latest job the checker created. Together they let the checker tell a job in
-- flight from a finished build.
ALTER TABLE package_build_state
    ADD COLUMN IF NOT EXISTS built_meta VARCHAR(64) NOT NULL DEFAULT '';

-- One row per job the checker created. ci_cnt numbers the distinct source
-- commits of a package unit in a project (CI_CNT); bcnt numbers the builds of
-- one source commit in one (project, repo, arch) (B_CNT).
CREATE TABLE IF NOT EXISTS build_counters (
    project       VARCHAR(128) NOT NULL,
    repo          VARCHAR(128) NOT NULL,
    arch          VARCHAR(64)  NOT NULL,
    package       VARCHAR(256) NOT NULL,
    flavor        VARCHAR(128) NOT NULL DEFAULT '',
    source_commit VARCHAR(64)  NOT NULL,
    meta          VARCHAR(64)  NOT NULL,
    ci_cnt        INTEGER      NOT NULL,
    bcnt          INTEGER      NOT NULL,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project, repo, arch, package, flavor, source_commit, meta)
);

CREATE INDEX IF NOT EXISTS idx_build_counters_unit
    ON build_counters(project, package, flavor, source_commit);

-- Forced rebuild requests (bsgo package rebuild), consumed by the checker.
CREATE TABLE IF NOT EXISTS build_force (
    project      VARCHAR(128) NOT NULL,
    repo         VARCHAR(128) NOT NULL DEFAULT '', -- empty: every repository
    arch         VARCHAR(64)  NOT NULL DEFAULT '', -- empty: every architecture
    package      VARCHAR(256) NOT NULL,
    flavor       VARCHAR(128) NOT NULL DEFAULT '',
    requested_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project, repo, arch, package, flavor)
);

-- Pointer to the current build repository index of a (project, repo, arch).
CREATE TABLE IF NOT EXISTS build_repo_index (
    project      VARCHAR(128) NOT NULL,
    repo         VARCHAR(128) NOT NULL,
    arch         VARCHAR(64)  NOT NULL,
    content_hash VARCHAR(64)  NOT NULL, -- sha256 of solv blob and entries
    solv_key     TEXT         NOT NULL,
    entries_key  TEXT         NOT NULL,
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project, repo, arch)
);

-- +goose Down
DROP TABLE IF EXISTS build_repo_index;
DROP TABLE IF EXISTS build_force;
DROP TABLE IF EXISTS build_counters;
ALTER TABLE package_build_state DROP COLUMN IF EXISTS built_meta;
