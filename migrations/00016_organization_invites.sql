-- +goose Up
-- Invitations for people who never signed in (spec 0001, R7.18, Marco 3b).
-- Someone who already has a user becomes a member at once and never gets a row
-- here. The first sign-in with this login turns the row into a membership.
CREATE TABLE organization_invites (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    login           text NOT NULL,
    invited_by      text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL
);
CREATE UNIQUE INDEX organization_invites_org_login ON organization_invites (organization_id, lower(login));
CREATE INDEX organization_invites_login ON organization_invites (lower(login));

-- +goose Down
DROP TABLE organization_invites;
