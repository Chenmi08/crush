-- +goose Up
ALTER TABLE sessions ADD COLUMN summary_fail_count INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE sessions DROP COLUMN summary_fail_count;
