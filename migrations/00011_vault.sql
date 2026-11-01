-- +goose Up
-- Vault (spec 0003, F2): the key Firstlume signs run JWTs with, each
-- organization's Vault connection and the inherited Vault role.

CREATE TABLE signing_keys (
    id          text PRIMARY KEY,
    -- PKCS#8, encrypted with the master key.
    private_enc bytea NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE vault_connections (
    organization_id uuid PRIMARY KEY REFERENCES organizations (id) ON DELETE CASCADE,
    address         text NOT NULL,
    ca_cert         text,
    namespace       text,
    auth_path       text NOT NULL DEFAULT 'jwt',
    audience        text NOT NULL DEFAULT 'vault',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE level_settings ADD COLUMN vault_role text CHECK (vault_role ~ '^[A-Za-z0-9._-]{1,128}$');

-- +goose Down
ALTER TABLE level_settings DROP COLUMN vault_role;
DROP TABLE vault_connections, signing_keys;
