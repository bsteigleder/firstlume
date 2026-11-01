-- +goose Up
-- Where each unit keeps its state, as the plan found after init (spec 0002
-- R2.3): [{unit, backend}]. NULL for runs from before it was recorded.
ALTER TABLE runs ADD COLUMN unit_backends jsonb;

-- +goose Down
ALTER TABLE runs DROP COLUMN unit_backends;
