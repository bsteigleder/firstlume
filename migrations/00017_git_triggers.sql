-- +goose Up
-- Git triggers (spec 0006, milestone G1): a push on a stack's branch starts a
-- run. What the webhook delivered is kept with the decision taken per stack.

-- Signs the webhooks of this connection. NULL until one is generated.
ALTER TABLE git_connections ADD COLUMN webhook_secret_enc bytea;

-- NULL inherits; the built-in default is off.
ALTER TABLE level_settings ADD COLUMN git_on_push text CHECK (git_on_push IN ('off', 'plan', 'apply'));

CREATE TABLE git_events (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Events of a branch are processed in the order they were received.
    seq             bigint GENERATED ALWAYS AS IDENTITY,
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    connection_id   uuid NOT NULL REFERENCES git_connections (id) ON DELETE CASCADE,
    delivery        text NOT NULL,
    kind            text NOT NULL CHECK (kind IN ('push')),
    source          text NOT NULL CHECK (source IN ('webhook')),
    repository      text NOT NULL,
    ref             text NOT NULL,
    before_sha      text NOT NULL,
    after_sha       text NOT NULL,
    total_commits   integer NOT NULL DEFAULT 0,
    sender          text NOT NULL,
    status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'done', 'failed')),
    attempts        integer NOT NULL DEFAULT 0,
    not_before      timestamptz NOT NULL DEFAULT now(),
    claimed_at      timestamptz,
    -- Why every stack of the branch counted as affected, when that happened.
    affects_all     text,
    error           text,
    received_at     timestamptz NOT NULL DEFAULT now(),
    processed_at    timestamptz,
    UNIQUE (connection_id, delivery)
);
CREATE INDEX git_events_pending_idx ON git_events (seq) WHERE status IN ('pending', 'processing');
CREATE INDEX git_events_org_idx ON git_events (organization_id, received_at DESC);

CREATE TABLE git_event_stacks (
    event_id   uuid NOT NULL REFERENCES git_events (id) ON DELETE CASCADE,
    -- The stack may be gone later; the event still says what was decided.
    stack_id   uuid REFERENCES stacks (id) ON DELETE SET NULL,
    stack_name text NOT NULL,
    outcome    text NOT NULL CHECK (outcome IN ('run_created', 'not_affected', 'trigger_off', 'other_branch', 'duplicate', 'error')),
    reason     text,
    files      jsonb,
    files_total integer NOT NULL DEFAULT 0,
    run_id     uuid REFERENCES runs (id) ON DELETE SET NULL
);
CREATE INDEX git_event_stacks_event_idx ON git_event_stacks (event_id);
CREATE INDEX git_event_stacks_stack_idx ON git_event_stacks (stack_id);

ALTER TABLE runs
    ADD COLUMN trigger_kind             text CHECK (trigger_kind IN ('push')),
    ADD COLUMN trigger_actor            text,
    ADD COLUMN trigger_event_id         uuid REFERENCES git_events (id) ON DELETE SET NULL,
    ADD COLUMN trigger_before_sha       text,
    ADD COLUMN trigger_pull_request     integer,
    ADD COLUMN trigger_pull_request_url text;
-- One run per stack and push, however many times the push is delivered.
CREATE UNIQUE INDEX runs_trigger_push_idx ON runs (stack_id, trigger_before_sha, commit_sha) WHERE trigger_kind = 'push';

-- +goose Down
DROP INDEX runs_trigger_push_idx;
ALTER TABLE runs
    DROP COLUMN trigger_kind, DROP COLUMN trigger_actor, DROP COLUMN trigger_event_id,
    DROP COLUMN trigger_before_sha, DROP COLUMN trigger_pull_request, DROP COLUMN trigger_pull_request_url;
DROP TABLE git_event_stacks;
DROP TABLE git_events;
ALTER TABLE level_settings DROP COLUMN git_on_push;
ALTER TABLE git_connections DROP COLUMN webhook_secret_enc;
