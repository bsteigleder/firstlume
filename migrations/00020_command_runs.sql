-- +goose Up
-- Command runs (spec 0009, canvas boards 68-70): a run that executes
-- Terraform, OpenTofu or Terragrunt subcommands from a closed list in the
-- stack's folder. It ends succeeded when every line exits 0.
ALTER TABLE runs DROP CONSTRAINT runs_type_check;
ALTER TABLE runs ADD CONSTRAINT runs_type_check CHECK (type IN ('plan', 'apply', 'destroy', 'drift', 'command'));
ALTER TABLE runs DROP CONSTRAINT runs_status_check;
ALTER TABLE runs ADD CONSTRAINT runs_status_check CHECK (status IN (
    'queued', 'planning', 'awaiting_approval', 'apply_queued', 'applying',
    'planned', 'no_changes', 'drifted', 'applied', 'failed', 'discarded', 'canceled', 'succeeded'));

-- The command as typed, and whether it may write the state (read: the plan
-- phase's token; write: the apply phase's).
ALTER TABLE runs ADD COLUMN command text;
ALTER TABLE runs ADD COLUMN state_access text CHECK (state_access IN ('read', 'write'));
ALTER TABLE runs ADD CONSTRAINT runs_command_fields
    CHECK ((type = 'command') = (command IS NOT NULL AND state_access IS NOT NULL));
-- How the command ended: the exit code of the line that failed, and that line.
ALTER TABLE runs ADD COLUMN exit_code integer;
ALTER TABLE runs ADD COLUMN failed_line integer;

-- +goose Down
DELETE FROM runs WHERE type = 'command';
ALTER TABLE runs DROP COLUMN failed_line;
ALTER TABLE runs DROP COLUMN exit_code;
ALTER TABLE runs DROP CONSTRAINT runs_command_fields;
ALTER TABLE runs DROP COLUMN state_access;
ALTER TABLE runs DROP COLUMN command;
ALTER TABLE runs DROP CONSTRAINT runs_status_check;
ALTER TABLE runs ADD CONSTRAINT runs_status_check CHECK (status IN (
    'queued', 'planning', 'awaiting_approval', 'apply_queued', 'applying',
    'planned', 'no_changes', 'drifted', 'applied', 'failed', 'discarded', 'canceled'));
ALTER TABLE runs DROP CONSTRAINT runs_type_check;
ALTER TABLE runs ADD CONSTRAINT runs_type_check CHECK (type IN ('plan', 'apply', 'destroy', 'drift'));
