-- +goose Up
-- Plan review (spec 0001, section 4.11): per-resource changes, confirming
-- destructive changes, what each run was configured with, and run steps.

ALTER TABLE level_settings
    -- NULL inherits. When true, approving needs every deleted or replaced resource confirmed.
    ADD COLUMN confirm_destructive boolean;

ALTER TABLE runs
    -- What the plan does, resource by resource (no sensitive values).
    ADD COLUMN changes jsonb,
    -- Keys of the deleted or replaced resources the approver confirmed.
    ADD COLUMN acknowledged jsonb,
    -- .firstlume/config.yaml as read at the run's commit.
    ADD COLUMN config jsonb,
    -- Effective settings and variable names (no secret values) the plan ran with.
    ADD COLUMN settings_used jsonb,
    -- The executor step whose output is being logged.
    ADD COLUMN current_step text;

-- Steps the executor reports (checkout, init, plan, apply, hooks...).
CREATE TABLE run_steps (
    run_id      uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    seq         integer NOT NULL,
    phase       text NOT NULL CHECK (phase IN ('plan', 'apply')),
    name        text NOT NULL,
    kind        text NOT NULL CHECK (kind IN ('core', 'hook')),
    status      text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'skipped')),
    started_at  timestamptz,
    finished_at timestamptz,
    PRIMARY KEY (run_id, seq)
);

ALTER TABLE run_log_chunks ADD COLUMN step text;

-- +goose Down
ALTER TABLE run_log_chunks DROP COLUMN step;
DROP TABLE run_steps;
ALTER TABLE runs DROP COLUMN changes, DROP COLUMN acknowledged, DROP COLUMN config,
    DROP COLUMN settings_used, DROP COLUMN current_step;
ALTER TABLE level_settings DROP COLUMN confirm_destructive;
