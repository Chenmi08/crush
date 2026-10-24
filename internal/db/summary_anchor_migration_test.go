package db

import (
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

// TestSummaryAnchorMigrationRoundTrip applies the PR-9 migration in both
// directions against the real embedded migration set. The columns are
// additive, but the down direction still has to be reversible: a checkpoint
// reverted during development must not leave the session service reading
// columns the schema no longer has.
func TestSummaryAnchorMigrationRoundTrip(t *testing.T) {
	// Not parallel: goose's state and the connection pool are shared.
	conn, err := Connect(t.Context(), t.TempDir())
	require.NoError(t, err)

	hasColumn := func(name string) bool {
		var count int
		require.NoError(t, conn.QueryRowContext(
			t.Context(),
			`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = ?`, name,
		).Scan(&count))
		return count > 0
	}

	require.True(t, hasColumn("summary_anchor_message_id"))
	require.True(t, hasColumn("summary_prompt_prefix_len"))

	// An existing row reads the default, and the anchor stays NULL because
	// only a summary that lands sets it.
	_, err = conn.ExecContext(t.Context(),
		`INSERT INTO sessions (id, title, prompt_tokens, completion_tokens, cost, message_count, created_at, updated_at)
		 VALUES ('s1', 't', 0, 0, 0, 0, 0, 0)`)
	require.NoError(t, err)

	var prefix int64
	require.NoError(t, conn.QueryRowContext(
		t.Context(),
		`SELECT summary_prompt_prefix_len FROM sessions WHERE id = 's1'`,
	).Scan(&prefix))
	require.Zero(t, prefix)

	require.NoError(t, goose.Down(conn, "migrations"))
	require.False(t, hasColumn("summary_anchor_message_id"))
	require.False(t, hasColumn("summary_prompt_prefix_len"))

	require.NoError(t, goose.Up(conn, "migrations"))
	require.True(t, hasColumn("summary_anchor_message_id"))
	require.True(t, hasColumn("summary_prompt_prefix_len"))
}
