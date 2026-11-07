-- +goose Up
CREATE TABLE IF NOT EXISTS users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    authentik_sub   VARCHAR(255) UNIQUE NOT NULL,
    username        VARCHAR(64) UNIQUE NOT NULL,
    email           VARCHAR(255) NOT NULL,
    is_superuser    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS groups (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            VARCHAR(64) UNIQUE NOT NULL,
    description     TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS personal_access_tokens (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name            VARCHAR(64) NOT NULL,
    token_hash      VARCHAR(64) NOT NULL UNIQUE,
    scopes          TEXT[] NOT NULL DEFAULT '{"build", "project:write"}',
    expires_at      TIMESTAMPTZ,
    last_used_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS project_maintainers (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_name    VARCHAR(128) NOT NULL,
    user_id         UUID REFERENCES users(id) ON DELETE CASCADE,
    group_id        UUID REFERENCES groups(id) ON DELETE CASCADE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_project_owner CHECK (
        (user_id IS NOT NULL AND group_id IS NULL) OR
        (user_id IS NULL AND group_id IS NOT NULL)
    ),
    CONSTRAINT uq_project_user UNIQUE (project_name, user_id),
    CONSTRAINT uq_project_group UNIQUE (project_name, group_id)
);
CREATE INDEX IF NOT EXISTS idx_project_maintainers ON project_maintainers(project_name);

CREATE TABLE IF NOT EXISTS project_access (
    project_name    VARCHAR(128) PRIMARY KEY,
    is_private      BOOLEAN NOT NULL DEFAULT FALSE,
    allowed_groups  TEXT[] DEFAULT '{}',
    allowed_users   TEXT[] DEFAULT '{}'
);

-- +goose Down
DROP TABLE IF EXISTS project_access;
DROP TABLE IF EXISTS project_maintainers;
DROP TABLE IF EXISTS personal_access_tokens;
DROP TABLE IF EXISTS groups;
DROP TABLE IF EXISTS users;
