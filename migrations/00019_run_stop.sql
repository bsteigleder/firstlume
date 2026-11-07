-- +goose Up
-- Stopping a run that is executing (canvas boards 66-67). The request is
-- recorded on the run; the executor learns of it on its next heartbeat,
-- interrupts Terraform and the run ends canceled. On a queued run it is who
-- canceled it.
ALTER TABLE runs ADD COLUMN stop_requested_at timestamptz;
ALTER TABLE runs ADD COLUMN stop_requested_by text;

-- +goose Down
ALTER TABLE runs DROP COLUMN stop_requested_by;
ALTER TABLE runs DROP COLUMN stop_requested_at;
