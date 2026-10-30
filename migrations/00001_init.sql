-- +goose Up
CREATE TABLE organizations (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- A template points at IaC code in a git repository and pins the tooling used to run it.
CREATE TABLE templates (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id    uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name               text NOT NULL,
    repository         text NOT NULL,
    path               text NOT NULL DEFAULT '',
    default_ref        text NOT NULL DEFAULT 'main',
    engine             text NOT NULL CHECK (engine IN ('terraform', 'opentofu')),
    engine_version     text NOT NULL,
    terragrunt_version text,
    git_token_enc      bytea,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name)
);

-- An environment is a deployed instance of a template (one state, one lock).
CREATE TABLE environments (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    template_id     uuid NOT NULL REFERENCES templates (id) ON DELETE RESTRICT,
    name            text NOT NULL,
    ref             text,
    auto_approve    boolean NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name)
);

CREATE TABLE environment_variables (
    environment_id uuid NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
    key            text NOT NULL,
    category       text NOT NULL CHECK (category IN ('env', 'terraform')),
    sensitive      boolean NOT NULL DEFAULT false,
    value          text,
    value_enc      bytea,
    PRIMARY KEY (environment_id, category, key),
    CHECK ((sensitive AND value IS NULL AND value_enc IS NOT NULL) OR (NOT sensitive AND value IS NOT NULL AND value_enc IS NULL))
);

CREATE TABLE agents (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name            text NOT NULL,
    token_hash      bytea NOT NULL UNIQUE,
    last_seen_at    timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name)
);

CREATE TABLE runs (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    environment_id  uuid NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
    type            text NOT NULL CHECK (type IN ('plan', 'apply', 'destroy')),
    status          text NOT NULL CHECK (status IN (
                        'queued', 'planning', 'awaiting_approval', 'apply_queued', 'applying',
                        'planned', 'no_changes', 'applied', 'failed', 'discarded', 'canceled')),
    ref             text NOT NULL,
    commit_sha      text,
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

CREATE INDEX runs_environment_idx ON runs (environment_id, created_at DESC);
CREATE INDEX runs_claimable_idx ON runs (created_at) WHERE status IN ('queued', 'apply_queued');
CREATE INDEX runs_active_idx ON runs (environment_id) WHERE status IN ('planning', 'awaiting_approval', 'apply_queued', 'applying');

CREATE TABLE run_log_chunks (
    id         bigserial PRIMARY KEY,
    run_id     uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    phase      text NOT NULL CHECK (phase IN ('plan', 'apply')),
    data       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX run_log_chunks_run_idx ON run_log_chunks (run_id, id);

-- Encrypted binary artifacts produced by runs (e.g. the saved plan file).
CREATE TABLE run_artifacts (
    run_id     uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    name       text NOT NULL,
    data_enc   bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, name)
);

-- +goose Down
DROP TABLE run_artifacts;
DROP TABLE run_log_chunks;
DROP TABLE runs;
DROP TABLE agents;
DROP TABLE environment_variables;
DROP TABLE environments;
DROP TABLE templates;
DROP TABLE organizations;
