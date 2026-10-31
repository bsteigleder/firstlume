-- +goose Up
-- Variables of category file become files in the stack's directory (spec 0003, R2.8).
ALTER TABLE variables DROP CONSTRAINT variables_category_check;
ALTER TABLE variables ADD CONSTRAINT variables_category_check CHECK (category IN ('env', 'terraform', 'file'));

-- +goose Down
DELETE FROM variables WHERE category = 'file';
ALTER TABLE variables DROP CONSTRAINT variables_category_check;
ALTER TABLE variables ADD CONSTRAINT variables_category_check CHECK (category IN ('env', 'terraform'));
