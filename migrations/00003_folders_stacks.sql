-- +goose Up
-- Organizations → folders → stacks, with settings inherited from the nearest
-- level (spec 0001). Replaces templates and environments. Firstlume has no
-- production data yet, so runs, environments and templates are dropped.

DROP TRIGGER runs_record_drift_result ON runs;
DROP FUNCTION record_drift_result();
DROP TABLE run_artifacts, run_log_chunks, runs, environment_variables, environments, templates;

-- Organizations are addressed by a slug in URLs; names need not be unique.
ALTER TABLE organizations ADD COLUMN slug text;
UPDATE organizations o SET slug = d.slug
FROM (
    SELECT id, CASE WHEN row_number() OVER (PARTITION BY base ORDER BY created_at) = 1 THEN base
                    ELSE trim(both '-' from left(base, 31)) || '-' || left(replace(id::text, '-', ''), 8) END AS slug
    FROM (
        SELECT id, created_at,
               coalesce(nullif(trim(both '-' from left(trim(both '-' from lower(regexp_replace(name, '[^a-zA-Z0-9]+', '-', 'g'))), 40)), ''), 'org') AS raw
        FROM organizations
    ) r,
    -- Slugs need at least two characters.
    LATERAL (SELECT CASE WHEN length(raw) < 2 THEN raw || '-org' ELSE raw END AS base) b
) d
WHERE d.id = o.id;
ALTER TABLE organizations
    ALTER COLUMN slug SET NOT NULL,
    ADD CONSTRAINT organizations_slug_key UNIQUE (slug),
    ADD CONSTRAINT organizations_slug_check CHECK (slug ~ '^[a-z0-9][a-z0-9-]*[a-z0-9]$' AND length(slug) BETWEEN 2 AND 40),
    DROP CONSTRAINT organizations_name_key;

CREATE TABLE folders (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    parent_id       uuid REFERENCES folders (id) ON DELETE RESTRICT,
    name            text NOT NULL,
    -- Ancestor ids from the top level down, ending with this folder. Folders
    -- cannot move, so it never changes; its length is the folder's depth.
    path            uuid[] NOT NULL CHECK (cardinality(path) BETWEEN 1 AND 10),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX folders_name_idx
    ON folders (organization_id, coalesce(parent_id, '00000000-0000-0000-0000-000000000000'), lower(name));

-- A stack is one managed infrastructure: the directory holding a .firstlume
-- folder, in a repository and branch. It has its own state, lock and runs.
CREATE TABLE stacks (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id    uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    folder_id          uuid REFERENCES folders (id) ON DELETE RESTRICT,
    name               text NOT NULL,
    repository         text NOT NULL,
    ref                text NOT NULL,
    path               text NOT NULL,
    git_token_enc      bytea,
    -- Last .firstlume/config.yaml read by a run, for display only.
    config             jsonb,
    -- When the scheduler last queued a drift run.
    drift_scheduled_at timestamptz,
    -- Outcome of the latest finished drift run.
    drift_status       text CHECK (drift_status IN ('in_sync', 'drifted', 'error')),
    drift_run_id       uuid,
    drift_checked_at   timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    -- Two stacks on the same code would manage the same infrastructure with separate states.
    UNIQUE (organization_id, repository, ref, path)
);
CREATE UNIQUE INDEX stacks_name_idx
    ON stacks (organization_id, coalesce(folder_id, '00000000-0000-0000-0000-000000000000'), lower(name));

-- Settings and variables can be set on the organization (folder_id and
-- stack_id both NULL), on a folder or on a stack. A stack uses, for each one,
-- the nearest level that sets it.
CREATE TABLE level_settings (
    organization_id        uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    folder_id              uuid REFERENCES folders (id) ON DELETE CASCADE,
    stack_id               uuid REFERENCES stacks (id) ON DELETE CASCADE,
    -- NULL inherits.
    require_approval       boolean,
    -- NULL inherits; 0 turns drift detection off.
    drift_interval_minutes integer CHECK (drift_interval_minutes = 0 OR drift_interval_minutes BETWEEN 15 AND 10080),
    updated_at             timestamptz NOT NULL DEFAULT now(),
    CHECK (folder_id IS NULL OR stack_id IS NULL)
);
CREATE UNIQUE INDEX level_settings_level_idx ON level_settings (organization_id,
    coalesce(folder_id, '00000000-0000-0000-0000-000000000000'), coalesce(stack_id, '00000000-0000-0000-0000-000000000000'));

CREATE TABLE variables (
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    folder_id       uuid REFERENCES folders (id) ON DELETE CASCADE,
    stack_id        uuid REFERENCES stacks (id) ON DELETE CASCADE,
    category        text NOT NULL CHECK (category IN ('env', 'terraform')),
    key             text NOT NULL,
    sensitive       boolean NOT NULL DEFAULT false,
    value           text,
    value_enc       bytea,
    CHECK (folder_id IS NULL OR stack_id IS NULL),
    CHECK ((sensitive AND value IS NULL AND value_enc IS NOT NULL) OR (NOT sensitive AND value IS NOT NULL AND value_enc IS NULL))
);
CREATE UNIQUE INDEX variables_level_key_idx ON variables (organization_id,
    coalesce(folder_id, '00000000-0000-0000-0000-000000000000'), coalesce(stack_id, '00000000-0000-0000-0000-000000000000'),
    category, key);

CREATE TABLE runs (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    stack_id        uuid NOT NULL REFERENCES stacks (id) ON DELETE CASCADE,
    type            text NOT NULL CHECK (type IN ('plan', 'apply', 'destroy', 'drift')),
    status          text NOT NULL CHECK (status IN (
                        'queued', 'planning', 'awaiting_approval', 'apply_queued', 'applying',
                        'planned', 'no_changes', 'drifted', 'applied', 'failed', 'discarded', 'canceled')),
    ref             text NOT NULL,
    commit_sha      text,
    -- Who wrote the commit and its subject line, as read by the executor.
    commit_author_name  text,
    commit_author_email text,
    commit_message      text,
    has_changes     boolean,
    plan_summary    jsonb,
    error           text,
    agent_id        uuid REFERENCES agents (id) ON DELETE SET NULL,
    created_by      text NOT NULL,
    approved_by     text,
    approved_at     timestamptz,
    claimed_at      timestamptz,
    heartbeat_at    timestamptz,
    started_at      timestamptz,
    finished_at     timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX runs_stack_idx ON runs (stack_id, created_at DESC);
CREATE INDEX runs_claimable_idx ON runs (created_at) WHERE status IN ('queued', 'apply_queued');
CREATE INDEX runs_active_idx ON runs (stack_id) WHERE status IN ('planning', 'awaiting_approval', 'apply_queued', 'applying');

ALTER TABLE stacks ADD CONSTRAINT stacks_drift_run_fk FOREIGN KEY (drift_run_id) REFERENCES runs (id) ON DELETE SET NULL;

CREATE TABLE run_log_chunks (
    id         bigserial PRIMARY KEY,
    run_id     uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    phase      text NOT NULL CHECK (phase IN ('plan', 'apply')),
    data       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX run_log_chunks_run_idx ON run_log_chunks (run_id, id);

-- Encrypted per-run files: the saved plan, and the variables the plan ran
-- with (the apply reuses them, whatever changed in between).
CREATE TABLE run_artifacts (
    run_id     uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    name       text NOT NULL,
    data_enc   bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, name)
);

-- Keeps the stack's drift summary current however a drift run ends.
-- +goose StatementBegin
CREATE FUNCTION record_drift_result() RETURNS trigger AS $$
BEGIN
    IF NEW.type = 'drift' AND NEW.status IS DISTINCT FROM OLD.status
       AND NEW.status IN ('no_changes', 'drifted', 'failed') THEN
        UPDATE stacks SET
            drift_status = CASE NEW.status WHEN 'no_changes' THEN 'in_sync' WHEN 'drifted' THEN 'drifted' ELSE 'error' END,
            drift_run_id = NEW.id,
            drift_checked_at = coalesce(NEW.finished_at, now())
        WHERE id = NEW.stack_id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER runs_record_drift_result AFTER UPDATE OF status ON runs
    FOR EACH ROW EXECUTE FUNCTION record_drift_result();

-- +goose Down
-- Not reversible: the templates/environments model and its data are gone.
SELECT 1;
