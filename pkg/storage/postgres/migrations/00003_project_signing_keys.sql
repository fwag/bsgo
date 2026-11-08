-- +goose Up
-- SQL in this section is executed after the migration is applied.

CREATE TABLE IF NOT EXISTS project_signing_keys (
    project_name VARCHAR(255) NOT NULL REFERENCES projects(name) ON DELETE CASCADE,
    key_flavor VARCHAR(64) NOT NULL DEFAULT 'default',
    key_type VARCHAR(64) NOT NULL DEFAULT 'gpg',
    public_key_armored TEXT NOT NULL,
    encrypted_private_key TEXT NOT NULL,
    fingerprint VARCHAR(128) NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project_name, key_flavor, key_type)
);

CREATE INDEX IF NOT EXISTS idx_project_signing_keys_proj ON project_signing_keys(project_name);

-- +goose Down
-- SQL in this section is executed when the migration is rolled back.

DROP TABLE IF EXISTS project_signing_keys;
