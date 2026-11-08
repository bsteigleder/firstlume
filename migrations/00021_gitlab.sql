-- +goose Up
-- GitLab as a Git provider (spec 0010).

ALTER TABLE git_connections DROP CONSTRAINT git_connections_provider_check;
ALTER TABLE git_connections ADD CONSTRAINT git_connections_provider_check CHECK (provider IN ('gitea', 'gitlab'));
-- A GitLab account is a group (with its subgroups) or a user.
ALTER TABLE git_connections DROP CONSTRAINT git_connections_account_kind_check;
ALTER TABLE git_connections ADD CONSTRAINT git_connections_account_kind_check CHECK (account_kind IN ('organization', 'group', 'user'));

-- When the webhook secret was made, for "Secret set 12 days ago" (R5.10).
ALTER TABLE git_connections ADD COLUMN webhook_secret_at timestamptz;
UPDATE git_connections SET webhook_secret_at = updated_at WHERE webhook_secret_enc IS NOT NULL;

-- A push whose last commit a stack already had a decision for (R5.9).
ALTER TABLE git_event_stacks DROP CONSTRAINT git_event_stacks_outcome_check;
ALTER TABLE git_event_stacks ADD CONSTRAINT git_event_stacks_outcome_check
    CHECK (outcome IN ('run_created', 'not_affected', 'trigger_off', 'other_branch', 'duplicate', 'already_handled', 'error'));
CREATE INDEX git_events_after_idx ON git_events (connection_id, repository, after_sha);

-- +goose Down
DROP INDEX git_events_after_idx;
DELETE FROM git_event_stacks WHERE outcome = 'already_handled';
ALTER TABLE git_event_stacks DROP CONSTRAINT git_event_stacks_outcome_check;
ALTER TABLE git_event_stacks ADD CONSTRAINT git_event_stacks_outcome_check
    CHECK (outcome IN ('run_created', 'not_affected', 'trigger_off', 'other_branch', 'duplicate', 'error'));
ALTER TABLE git_connections DROP COLUMN webhook_secret_at;
ALTER TABLE git_connections DROP CONSTRAINT git_connections_account_kind_check;
ALTER TABLE git_connections ADD CONSTRAINT git_connections_account_kind_check CHECK (account_kind IN ('organization', 'user'));
ALTER TABLE git_connections DROP CONSTRAINT git_connections_provider_check;
ALTER TABLE git_connections ADD CONSTRAINT git_connections_provider_check CHECK (provider IN ('gitea'));
