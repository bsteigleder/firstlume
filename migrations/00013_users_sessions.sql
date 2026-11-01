-- +goose Up
-- Login through an OIDC provider, Gitea first (spec 0001, R7.1 to R7.3,
-- R7.11, Marco 3a).

-- One row per person, keyed by the provider's subject. Groups are re-read on
-- every login (R7.2).
CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    issuer        text NOT NULL,
    subject       text NOT NULL,
    login         text NOT NULL,
    name          text NOT NULL DEFAULT '',
    email         text NOT NULL DEFAULT '',
    avatar_url    text NOT NULL DEFAULT '',
    groups        text[] NOT NULL DEFAULT '{}',
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_login_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (issuer, subject)
);

-- Server-side sessions: only the cookie's hash is stored, so a session can be
-- revoked (R7.3).
CREATE TABLE sessions (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz
);
CREATE INDEX sessions_user ON sessions (user_id);

-- A login in progress: the OAuth state is single use, so the callback deletes
-- the row it consumes (CA3.2). Every replica sees it.
CREATE TABLE login_attempts (
    state_hash    bytea PRIMARY KEY,
    code_verifier text NOT NULL,
    nonce         text NOT NULL,
    return_to     text NOT NULL DEFAULT '/',
    expires_at    timestamptz NOT NULL
);

-- Who belongs to an organization. Marco 3a only records the creator (R7.7);
-- invitations and removal come with Marco 3b.
CREATE TABLE organization_members (
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    user_id         uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role            text NOT NULL DEFAULT 'admin' CHECK (role = 'admin'),
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, user_id)
);
CREATE INDEX organization_members_user ON organization_members (user_id);

-- +goose Down
DROP TABLE organization_members;
DROP TABLE login_attempts;
DROP TABLE sessions;
DROP TABLE users;
