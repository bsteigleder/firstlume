-- +goose Up
-- Registry credentials (spec 0003, F3), set on the organization, a folder or
-- a stack and inherited by host. The password is stored encrypted, or comes
-- from Vault, read by the executor.
CREATE TABLE registry_credentials (
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    folder_id       uuid REFERENCES folders (id) ON DELETE CASCADE,
    stack_id        uuid REFERENCES stacks (id) ON DELETE CASCADE,
    host            text NOT NULL,
    kind            text NOT NULL CHECK (kind IN ('oci', 'terraform')),
    username        text,
    password_enc    bytea,
    vault_path      text,
    vault_field     text,
    CHECK (folder_id IS NULL OR stack_id IS NULL),
    CHECK ((password_enc IS NULL) <> (vault_path IS NULL AND vault_field IS NULL))
);
CREATE UNIQUE INDEX registry_credentials_level_host_idx ON registry_credentials (organization_id,
    coalesce(folder_id, '00000000-0000-0000-0000-000000000000'), coalesce(stack_id, '00000000-0000-0000-0000-000000000000'),
    host);

-- +goose Down
DROP TABLE registry_credentials;
