-- +goose Up
-- State storage per organization (spec 0005): an organization may keep its
-- state versions in its own S3 bucket, inside a folder Firstlume owns, named
-- after its folders, stacks and units, encrypted or in the clear. Each version
-- remembers where it is and how it was written, so changing the storage or the
-- mode never makes an older version unreadable.
CREATE TABLE state_storages (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id       uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    endpoint              text NOT NULL,
    bucket                text NOT NULL,
    region                text NOT NULL,
    -- The folder inside the bucket; Firstlume touches nothing outside it (R1.5).
    folder                text NOT NULL,
    path_style            boolean NOT NULL DEFAULT true,
    ca_cert               text,
    access_key_id         text NOT NULL,
    -- Encrypted with the master key; never returned by the API.
    secret_access_key_enc bytea NOT NULL,
    -- New versions are encrypted (true) or written as plain terraform.tfstate (R5.2).
    encrypt               boolean NOT NULL DEFAULT true,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX state_storages_location ON state_storages (endpoint, bucket, folder);

-- The organization's current storage; NULL is the installation's (R1.1).
ALTER TABLE organizations ADD COLUMN state_storage_id uuid REFERENCES state_storages (id) ON DELETE SET NULL;

-- Where each version is (NULL: the installation's storage) and whether it is
-- encrypted (R4.1, R5.2).
ALTER TABLE state_versions
    ADD COLUMN storage_id uuid REFERENCES state_storages (id),
    ADD COLUMN encrypted  boolean NOT NULL DEFAULT true;
CREATE INDEX state_versions_storage ON state_versions (storage_id) WHERE storage_id IS NOT NULL;

-- Organization-wide state events, such as a storage change, have no stack.
ALTER TABLE state_events ALTER COLUMN stack_id DROP NOT NULL;
CREATE INDEX state_events_org ON state_events (organization_id, created_at) WHERE stack_id IS NULL;

-- +goose Down
DROP INDEX state_events_org;
DELETE FROM state_events WHERE stack_id IS NULL;
ALTER TABLE state_events ALTER COLUMN stack_id SET NOT NULL;
DROP INDEX state_versions_storage;
ALTER TABLE state_versions DROP COLUMN encrypted, DROP COLUMN storage_id;
ALTER TABLE organizations DROP COLUMN state_storage_id;
DROP TABLE state_storages;
