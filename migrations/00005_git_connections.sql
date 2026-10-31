-- +goose Up
-- An organization's Git connection (spec 0001, 4.5): the Gitea account whose
-- repositories it manages, reached with a service token that is separate
-- from anyone's login.
CREATE TABLE git_connections (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- One per organization for now; the model allows several later.
    organization_id uuid NOT NULL UNIQUE REFERENCES organizations (id) ON DELETE CASCADE,
    provider        text NOT NULL CHECK (provider IN ('gitea')),
    url             text NOT NULL,
    account         text NOT NULL,
    account_kind    text NOT NULL CHECK (account_kind IN ('organization', 'user')),
    token_enc       bytea NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- Stacks created from the connection clone with its service token.
ALTER TABLE stacks ADD COLUMN connection_id uuid REFERENCES git_connections (id) ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE stacks DROP COLUMN connection_id;
DROP TABLE git_connections;
