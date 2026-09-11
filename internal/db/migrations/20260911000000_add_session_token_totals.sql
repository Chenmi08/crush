-- +goose Up
-- +goose StatementBegin
-- Cumulative token usage for the whole session. Unlike prompt_tokens and
-- completion_tokens, which describe the most recent request's context,
-- these counters accumulate over every model step.
ALTER TABLE sessions ADD COLUMN total_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (total_input_tokens >= 0);
ALTER TABLE sessions ADD COLUMN total_output_tokens INTEGER NOT NULL DEFAULT 0 CHECK (total_output_tokens >= 0);
ALTER TABLE sessions ADD COLUMN cache_read_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cache_read_tokens >= 0);
ALTER TABLE sessions ADD COLUMN cache_creation_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cache_creation_tokens >= 0);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE sessions DROP COLUMN total_input_tokens;
ALTER TABLE sessions DROP COLUMN total_output_tokens;
ALTER TABLE sessions DROP COLUMN cache_read_tokens;
ALTER TABLE sessions DROP COLUMN cache_creation_tokens;
-- +goose StatementEnd
