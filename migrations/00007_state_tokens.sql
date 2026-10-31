-- +goose Up
-- State tokens (spec 0002, R7): let a pipeline outside Firstlume use a
-- stack's state ("state only" mode). Only the token's hash is stored.

CREATE TABLE state_tokens (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    stack_id        uuid NOT NULL REFERENCES stacks (id) ON DELETE CASCADE,
    name            text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    -- Read-only tokens can only GET (R7.3).
    can_write       boolean NOT NULL,
    token_hash      bytea NOT NULL UNIQUE,
    created_by      text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz,
    last_used_at    timestamptz,
    revoked_at      timestamptz
);
CREATE UNIQUE INDEX state_tokens_stack_name ON state_tokens (stack_id, name) WHERE revoked_at IS NULL;

-- The token that wrote a version, when one did (R7.5).
ALTER TABLE state_versions ADD COLUMN token_id uuid;

-- +goose Down
ALTER TABLE state_versions DROP COLUMN token_id;
DROP TABLE state_tokens;
