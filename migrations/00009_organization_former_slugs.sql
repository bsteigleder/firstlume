-- +goose Up
-- An organization can change its address. The old addresses keep resolving
-- to it (bookmarks, FIRSTLUME_ORGANIZATION), so no other organization may
-- take them; the organization itself may take one back.

CREATE TABLE organization_former_slugs (
    slug            text PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    created_at      timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE organization_former_slugs;
