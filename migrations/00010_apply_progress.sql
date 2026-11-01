-- +goose Up
-- Apply followed resource by resource (spec 0004). The executor reports what
-- Terraform did to each resource; the state is derived when reading, from
-- these facts and the plan's action.

ALTER TABLE runs
    -- True once the apply reported progress; runs without it show as before.
    ADD COLUMN apply_tracked boolean NOT NULL DEFAULT false;

CREATE TABLE run_change_progress (
    run_id     uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    -- ResourceChange key: unit:address, or the address.
    key        text NOT NULL,
    -- Operations in the order they ran: [{action, started_at, finished_at, failed}].
    steps      jsonb NOT NULL DEFAULT '[]',
    -- Errors Terraform reported for the resource: [{summary, detail, file, line}].
    errors     jsonb NOT NULL DEFAULT '[]',
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, key)
);

-- +goose Down
DROP TABLE run_change_progress;
ALTER TABLE runs DROP COLUMN apply_tracked;
