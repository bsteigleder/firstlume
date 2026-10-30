-- +goose Up
-- Drift detection: scheduled plan-only runs that report, and never change, infrastructure.

ALTER TABLE runs DROP CONSTRAINT runs_type_check;
ALTER TABLE runs ADD CONSTRAINT runs_type_check CHECK (type IN ('plan', 'apply', 'destroy', 'drift'));
ALTER TABLE runs DROP CONSTRAINT runs_status_check;
ALTER TABLE runs ADD CONSTRAINT runs_status_check CHECK (status IN (
    'queued', 'planning', 'awaiting_approval', 'apply_queued', 'applying',
    'planned', 'no_changes', 'drifted', 'applied', 'failed', 'discarded', 'canceled'));

ALTER TABLE environments
    -- NULL disables scheduled drift detection.
    ADD COLUMN drift_interval_minutes integer CHECK (drift_interval_minutes BETWEEN 15 AND 10080),
    -- When the scheduler last queued a drift run; the next one is due an interval later.
    ADD COLUMN drift_scheduled_at timestamptz,
    -- Outcome of the latest finished drift run.
    ADD COLUMN drift_status text CHECK (drift_status IN ('in_sync', 'drifted', 'error')),
    ADD COLUMN drift_run_id uuid REFERENCES runs (id) ON DELETE SET NULL,
    ADD COLUMN drift_checked_at timestamptz;

-- Keeps the environment's drift summary current however a drift run ends
-- (completed by the executor, failed by the agent or by the heartbeat sweeper).
-- +goose StatementBegin
CREATE FUNCTION record_drift_result() RETURNS trigger AS $$
BEGIN
    IF NEW.type = 'drift' AND NEW.status IS DISTINCT FROM OLD.status
       AND NEW.status IN ('no_changes', 'drifted', 'failed') THEN
        UPDATE environments SET
            drift_status = CASE NEW.status WHEN 'no_changes' THEN 'in_sync' WHEN 'drifted' THEN 'drifted' ELSE 'error' END,
            drift_run_id = NEW.id,
            drift_checked_at = coalesce(NEW.finished_at, now())
        WHERE id = NEW.environment_id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER runs_record_drift_result AFTER UPDATE OF status ON runs
    FOR EACH ROW EXECUTE FUNCTION record_drift_result();

CREATE INDEX environments_drift_due_idx ON environments (drift_scheduled_at) WHERE drift_interval_minutes IS NOT NULL;

-- +goose Down
DROP TRIGGER runs_record_drift_result ON runs;
DROP FUNCTION record_drift_result();
ALTER TABLE environments
    DROP COLUMN drift_interval_minutes, DROP COLUMN drift_scheduled_at,
    DROP COLUMN drift_status, DROP COLUMN drift_run_id, DROP COLUMN drift_checked_at;
DELETE FROM runs WHERE type = 'drift';
ALTER TABLE runs DROP CONSTRAINT runs_status_check;
ALTER TABLE runs ADD CONSTRAINT runs_status_check CHECK (status IN (
    'queued', 'planning', 'awaiting_approval', 'apply_queued', 'applying',
    'planned', 'no_changes', 'applied', 'failed', 'discarded', 'canceled'));
ALTER TABLE runs DROP CONSTRAINT runs_type_check;
ALTER TABLE runs ADD CONSTRAINT runs_type_check CHECK (type IN ('plan', 'apply', 'destroy'));
