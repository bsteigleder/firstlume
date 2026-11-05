-- +goose Up
-- Where runs execute (spec 0007): node selector, tolerations and affinities, at
-- the organization, folder, stack and agent. A field that is absent inherits.

-- NULL defines nothing at this level.
ALTER TABLE level_settings ADD COLUMN placement jsonb;

-- Set on the agent in the UI or the API.
ALTER TABLE agents ADD COLUMN placement jsonb NOT NULL DEFAULT '{}';
-- What the agent reports when it asks for work. NULL until it does.
ALTER TABLE agents ADD COLUMN accepts_placement boolean;
ALTER TABLE agents ADD COLUMN install_placement jsonb;

-- What the organization, folders and stack defined when the run was created.
-- The apply uses the same as the plan, whatever changed in between (R2.2).
ALTER TABLE runs ADD COLUMN placement jsonb NOT NULL DEFAULT '{}';
-- All the last phase used, with the agent's and the installation's values. The
-- agent can change later, so this can't be worked out when the run is read.
ALTER TABLE runs ADD COLUMN placement_used jsonb;

-- +goose Down
ALTER TABLE runs DROP COLUMN placement_used;
ALTER TABLE runs DROP COLUMN placement;
ALTER TABLE agents DROP COLUMN install_placement;
ALTER TABLE agents DROP COLUMN accepts_placement;
ALTER TABLE agents DROP COLUMN placement;
ALTER TABLE level_settings DROP COLUMN placement;
