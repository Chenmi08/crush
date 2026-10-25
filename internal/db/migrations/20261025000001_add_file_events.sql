-- +goose Up
-- +goose StatementBegin
-- A write/exec ledger next to read_files. Reads stay in read_files because
-- require-read-before-write depends on that table's semantics: it answers
-- "did this session see the file before writing it", which a write event
-- cannot. This table answers the other question: "what did this session
-- change", which is what a resuming agent needs after compaction.
--
-- path holds a working-directory-relative file path for kind='write' and the
-- command line for kind='exec'. One row per (session, path, kind): repeating
-- a write refreshes at instead of appending, so the ledger stays bounded to
-- the files and commands a session actually touched.
--
-- kind also admits 'read', which nothing writes yet: read_files keeps that
-- answer, and a CHECK constraint cannot be widened without rebuilding the
-- table, so the value is reserved now rather than migrated later.
CREATE TABLE IF NOT EXISTS file_events (
    session_id TEXT NOT NULL CHECK (session_id != ''),
    path TEXT NOT NULL CHECK (path != ''),
    kind TEXT NOT NULL CHECK (kind IN ('read', 'write', 'exec')),
    at INTEGER NOT NULL,  -- Unix timestamp in milliseconds of the latest event
    FOREIGN KEY (session_id) REFERENCES sessions (id) ON DELETE CASCADE,
    PRIMARY KEY (session_id, path, kind)
);

CREATE INDEX IF NOT EXISTS idx_file_events_session_kind_at
    ON file_events (session_id, kind, at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_file_events_session_kind_at;
DROP TABLE IF EXISTS file_events;
-- +goose StatementEnd
