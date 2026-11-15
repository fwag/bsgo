-- +goose Up
-- OpenPGP project keys (plan 02, S1). public_key_armored and fingerprint
-- already exist and now hold the armored OpenPGP public key and the v4
-- fingerprint. Rows written before this migration hold raw RSA keys: they
-- keep algo = '' and the signer treats them as absent, regenerating the key
-- on first use. No consumer ever published a signature made with them.

ALTER TABLE project_signing_keys ADD COLUMN IF NOT EXISTS algo VARCHAR(32) NOT NULL DEFAULT '';
ALTER TABLE project_signing_keys ADD COLUMN IF NOT EXISTS expires_at TIMESTAMP WITH TIME ZONE;

-- +goose Down
ALTER TABLE project_signing_keys DROP COLUMN IF EXISTS expires_at;
ALTER TABLE project_signing_keys DROP COLUMN IF EXISTS algo;
