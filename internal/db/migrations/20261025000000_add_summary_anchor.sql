-- +goose Up
-- +goose StatementBegin
-- Where the current summary starts, and how much of it is model prose.
--
-- summary_anchor_message_id is the id of the oldest message the current
-- summary replaces: the start of the span the next compaction merges into.
-- summary_prompt_prefix_len is the byte length of the model-written part of
-- the summary's content; the context manifest and the transcript pointer are
-- appended after it, so this is how later code recovers the prose without
-- searching for the boundary.
ALTER TABLE sessions ADD COLUMN summary_anchor_message_id TEXT;
ALTER TABLE sessions ADD COLUMN summary_prompt_prefix_len INTEGER NOT NULL DEFAULT 0 CHECK (summary_prompt_prefix_len >= 0);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE sessions DROP COLUMN summary_prompt_prefix_len;
ALTER TABLE sessions DROP COLUMN summary_anchor_message_id;
-- +goose StatementEnd
