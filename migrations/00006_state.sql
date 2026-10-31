-- +goose Up
-- Firstlume's Terraform state backend (spec 0002): one state per unit of a
-- stack, with a lock and immutable versions. The bytes of each version live in
-- the configured storage (state_objects for Postgres, or an S3 bucket),
-- always encrypted.

CREATE TABLE state_units (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    -- No foreign key: a deleted stack's state is kept for 30 days (R6.2).
    stack_id        uuid NOT NULL,
    -- Path of the unit relative to the stack; empty for the root.
    key             text NOT NULL CHECK (length(key) <= 512),
    -- 0 until the first write.
    current_version integer NOT NULL DEFAULT 0,
    -- The lock Terraform holds, if any: its ID, the LockInfo it sent and who holds it.
    lock_id         text,
    lock_info       jsonb,
    lock_run_id     uuid,
    lock_holder     text,
    locked_at       timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    deleted_at      timestamptz
);
CREATE UNIQUE INDEX state_units_stack_key ON state_units (stack_id, key) WHERE deleted_at IS NULL;
CREATE INDEX state_units_lock_run ON state_units (lock_run_id) WHERE lock_run_id IS NOT NULL;

CREATE TABLE state_versions (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    unit_id     uuid NOT NULL REFERENCES state_units (id) ON DELETE CASCADE,
    number      integer NOT NULL,
    serial      bigint NOT NULL,
    lineage     text NOT NULL,
    sha256      bytea NOT NULL,
    size        integer NOT NULL,
    -- Managed resources in the state.
    resources   integer NOT NULL,
    object_key  text NOT NULL,
    -- Who wrote it: a run phase, or (later) a state token.
    run_id      uuid,
    phase       text,
    written_by  text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (unit_id, number)
);

-- Encrypted state versions when the storage is Postgres.
CREATE TABLE state_objects (
    key        text PRIMARY KEY,
    data       bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- What happened to a stack's state besides writes (R8.7).
CREATE TABLE state_events (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    stack_id        uuid NOT NULL,
    unit_key        text,
    kind            text NOT NULL,
    actor           text NOT NULL,
    detail          jsonb,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX state_events_stack ON state_events (stack_id, created_at);

-- A run phase's locks are released when it stops executing, however it ends
-- (completed, failed, heartbeat timeout): R5.3.
-- +goose StatementBegin
CREATE FUNCTION release_run_state_locks() RETURNS trigger AS $$
BEGIN
    IF OLD.status IN ('planning', 'applying') AND NEW.status IS DISTINCT FROM OLD.status THEN
        WITH released AS (
            UPDATE state_units SET lock_id = NULL, lock_info = NULL, lock_run_id = NULL, lock_holder = NULL,
                   locked_at = NULL, updated_at = now()
            WHERE lock_run_id = NEW.id
            RETURNING organization_id, stack_id, key
        )
        INSERT INTO state_events (organization_id, stack_id, unit_key, kind, actor, detail)
        SELECT organization_id, stack_id, key, 'lock_released', 'firstlume',
               jsonb_build_object('reason', 'run ended', 'run_id', NEW.id)
        FROM released;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER runs_release_state_locks AFTER UPDATE OF status ON runs
    FOR EACH ROW EXECUTE FUNCTION release_run_state_locks();

-- +goose Down
DROP TRIGGER runs_release_state_locks ON runs;
DROP FUNCTION release_run_state_locks();
DROP TABLE state_events, state_objects, state_versions, state_units;
