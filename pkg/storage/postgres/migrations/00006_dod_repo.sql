-- +goose Up
CREATE TABLE IF NOT EXISTS dod_repo (
    url             TEXT         NOT NULL PRIMARY KEY,
    name            VARCHAR(256) NOT NULL DEFAULT '',
    repomd_checksum VARCHAR(64)  NOT NULL, -- sha256 of repomd.xml
    solv_key        TEXT         NOT NULL, -- S3 key dod/<sha256(url)>/<primary-checksum>.solv
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS dod_repo;
