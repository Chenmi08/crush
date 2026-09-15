-- +goose Up
-- +goose StatementBegin
-- Per-session skill opt-outs. Stored as a JSON array of skill names so a
-- session can enable or disable individual skills independently of the
-- global options.disabled_skills default it was seeded from.
ALTER TABLE sessions ADD COLUMN disabled_skills TEXT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE sessions DROP COLUMN disabled_skills;
-- +goose StatementEnd
