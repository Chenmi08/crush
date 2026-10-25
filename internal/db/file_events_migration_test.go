package db

import (
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

// TestFileEventsMigrationRoundTrip applies the PR-12 migration in both
// directions against the real embedded migration set. The table is additive,
// but the down direction has to leave no trace: a checkpoint reverted during
// development must not leave a ledger the filetracker still writes to.
func TestFileEventsMigrationRoundTrip(t *testing.T) {
	// Not parallel: goose's state and the connection pool are shared.
	conn, err := Connect(t.Context(), t.TempDir())
	require.NoError(t, err)

	hasTable := func() bool {
		var count int
		require.NoError(t, conn.QueryRowContext(
			t.Context(),
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'file_events'`,
		).Scan(&count))
		return count > 0
	}
	require.True(t, hasTable())

	// The ledger cascades with its session, so a deleted session leaves no
	// file paths behind.
	_, err = conn.ExecContext(t.Context(),
		`INSERT INTO sessions (id, title, prompt_tokens, completion_tokens, cost, message_count, created_at, updated_at)
		 VALUES ('s1', 't', 0, 0, 0, 0, 0, 0)`)
	require.NoError(t, err)
	_, err = conn.ExecContext(t.Context(),
		`INSERT INTO file_events (session_id, path, kind, at) VALUES ('s1', 'a.go', 'write', 1)`)
	require.NoError(t, err)

	_, err = conn.ExecContext(t.Context(), `DELETE FROM sessions WHERE id = 's1'`)
	require.NoError(t, err)

	var remaining int
	require.NoError(t, conn.QueryRowContext(
		t.Context(), `SELECT COUNT(*) FROM file_events`,
	).Scan(&remaining))
	require.Zero(t, remaining, "deleting the session clears its ledger")

	require.NoError(t, goose.DownTo(conn, "migrations", 20261025000000))
	require.False(t, hasTable())

	require.NoError(t, goose.Up(conn, "migrations"))
	require.True(t, hasTable())
}

// The kind column only accepts the three documented values, so a typo cannot
// silently create a fourth class of event.
func TestFileEventsKindIsConstrained(t *testing.T) {
	// Not parallel: goose's state and the connection pool are shared.
	conn, err := Connect(t.Context(), t.TempDir())
	require.NoError(t, err)

	_, err = conn.ExecContext(t.Context(),
		`INSERT INTO sessions (id, title, prompt_tokens, completion_tokens, cost, message_count, created_at, updated_at)
		 VALUES ('s1', 't', 0, 0, 0, 0, 0, 0)`)
	require.NoError(t, err)

	_, err = conn.ExecContext(t.Context(),
		`INSERT INTO file_events (session_id, path, kind, at) VALUES ('s1', 'a.go', 'read', 1)`)
	require.NoError(t, err, "read is a documented kind even though read_files holds it today")

	_, err = conn.ExecContext(t.Context(),
		`INSERT INTO file_events (session_id, path, kind, at) VALUES ('s1', 'a.go', 'bogus', 1)`)
	require.Error(t, err)
}
