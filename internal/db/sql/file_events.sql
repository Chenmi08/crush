-- name: RecordFileEvent :exec
-- One row per (session, path, kind), so a repeated write moves the timestamp
-- forward instead of adding an event. `kind` is validated by the table.
--
-- `at` is written by the caller in Unix milliseconds. Second resolution is
-- not enough to order the edits a multi-file change makes in one burst, and
-- the summary lists the newest five.
INSERT INTO file_events (
    session_id,
    path,
    kind,
    at
) VALUES (
    ?,
    ?,
    ?,
    ?
) ON CONFLICT(session_id, path, kind) DO UPDATE SET
    at = excluded.at;

-- name: ListRecentFileWrites :many
-- Backs the summary's "recently modified files" section: the files this
-- session wrote, newest first. Ties break on path so an edit that lands in
-- the same millisecond as another still renders in a stable order.
SELECT *
FROM file_events
WHERE session_id = ? AND kind = 'write'
ORDER BY at DESC, path ASC
LIMIT ?;
