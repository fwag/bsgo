-- +goose Up
-- The revision log of a package: one entry per change of its pinned (git url,
-- commit), like an OBS source revision. vrev (the CI_CNT of its builds) is
-- resolved asynchronously from the first-parent git history, see
-- docs/plans/04-release-counters.md.
CREATE TABLE IF NOT EXISTS package_revisions (
    project       VARCHAR(128) NOT NULL,
    package       VARCHAR(256) NOT NULL,
    seq           INTEGER      NOT NULL,
    git_url       TEXT         NOT NULL,
    commit_sha    VARCHAR(64)  NOT NULL,
    state         VARCHAR(16)  NOT NULL DEFAULT 'pending', -- pending | resolved | skipped
    version       TEXT,
    git_vrev      INTEGER,
    vrev          INTEGER,
    min_vrev      INTEGER,
    source        VARCHAR(16),                             -- git | admin
    attempts      INTEGER      NOT NULL DEFAULT 0,
    last_error    TEXT,
    next_try_at   TIMESTAMPTZ,
    claimed_by    TEXT,
    claimed_until TIMESTAMPTZ,
    wanted_at     TIMESTAMPTZ,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    resolved_at   TIMESTAMPTZ,
    PRIMARY KEY (project, package, seq)
);
CREATE INDEX IF NOT EXISTS idx_package_revisions_commit
    ON package_revisions(project, package, commit_sha, seq DESC);
CREATE INDEX IF NOT EXISTS idx_package_revisions_pending
    ON package_revisions(state, next_try_at) WHERE state = 'pending';

-- Memo of the version history fold, per requested commit.
CREATE TABLE IF NOT EXISTS source_history (
    url           TEXT        NOT NULL,
    commit_sha    VARCHAR(64) NOT NULL,
    lookup        TEXT        NOT NULL,  -- package name, or the pinned recipe
    algo_rev      INTEGER     NOT NULL,
    config_hash   TEXT        NOT NULL,
    main_recipe   TEXT        NOT NULL,  -- '' when none
    version       TEXT        NOT NULL,
    release_start INTEGER     NOT NULL,
    vt            JSONB       NOT NULL,
    walked        INTEGER     NOT NULL,
    computed_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (url, commit_sha, lookup, algo_rev, config_hash)
);

-- NULL versrel marks a counter row written before revisions existed.
ALTER TABLE build_counters ADD COLUMN IF NOT EXISTS versrel TEXT;
CREATE INDEX IF NOT EXISTS idx_build_counters_versrel
    ON build_counters(project, repo, arch, package, flavor, versrel);
ALTER TABLE package_build_state ADD COLUMN IF NOT EXISTS versrel TEXT NOT NULL DEFAULT '';

-- One pending revision per existing package pin; the resolver works them off.
INSERT INTO package_revisions (project, package, seq, git_url, commit_sha)
SELECT project_name, package_name, 1, git_url, commit_sha FROM project_packages
WHERE git_url <> '' AND commit_sha <> ''
ON CONFLICT DO NOTHING;

-- +goose Down
ALTER TABLE package_build_state DROP COLUMN IF EXISTS versrel;
DROP INDEX IF EXISTS idx_build_counters_versrel;
ALTER TABLE build_counters DROP COLUMN IF EXISTS versrel;
DROP TABLE IF EXISTS source_history;
DROP TABLE IF EXISTS package_revisions;
