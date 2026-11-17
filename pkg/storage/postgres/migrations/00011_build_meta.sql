-- +goose Up
-- Dependency meta of builds (the OBS ":meta" files): the lines a build was
-- scheduled with, gzip compressed and addressed by their hash, which
-- package_build_state.meta and built_meta hold. Dependent builds include
-- them in their own meta, so they are kept apart from the state rows.
CREATE TABLE IF NOT EXISTS build_meta (
    project VARCHAR(128) NOT NULL,
    repo    VARCHAR(128) NOT NULL,
    arch    VARCHAR(64)  NOT NULL,
    hash    VARCHAR(64)  NOT NULL,
    lines   BYTEA        NOT NULL,
    PRIMARY KEY (project, repo, arch, hash)
);

-- +goose Down
DROP TABLE IF EXISTS build_meta;
