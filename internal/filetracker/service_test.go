package filetracker

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/stretchr/testify/require"
)

type testEnv struct {
	ctx context.Context
	q   *db.Queries
	svc Service
	// conn backs the row-count assertions no generated query exposes.
	conn *sql.DB
}

func setupTest(t *testing.T) *testEnv {
	t.Helper()

	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	q := db.New(conn)
	return &testEnv{
		ctx:  t.Context(),
		q:    q,
		svc:  NewService(q),
		conn: conn,
	}
}

func (e *testEnv) createSession(t *testing.T, sessionID string) {
	t.Helper()
	_, err := e.q.CreateSession(e.ctx, db.CreateSessionParams{
		ID:    sessionID,
		Title: "Test Session",
	})
	require.NoError(t, err)
}

func TestService_RecordRead(t *testing.T) {
	env := setupTest(t)

	sessionID := "test-session-1"
	path := "/path/to/file.go"
	env.createSession(t, sessionID)

	env.svc.RecordRead(env.ctx, sessionID, path)

	lastRead := env.svc.LastReadTime(env.ctx, sessionID, path)
	require.False(t, lastRead.IsZero(), "expected non-zero time after recording read")
	require.WithinDuration(t, time.Now(), lastRead, 2*time.Second)
}

func TestService_LastReadTime_NotFound(t *testing.T) {
	env := setupTest(t)

	lastRead := env.svc.LastReadTime(env.ctx, "nonexistent-session", "/nonexistent/path")
	require.True(t, lastRead.IsZero(), "expected zero time for unread file")
}

func TestService_RecordRead_UpdatesTimestamp(t *testing.T) {
	env := setupTest(t)

	sessionID := "test-session-2"
	path := "/path/to/file.go"
	env.createSession(t, sessionID)

	env.svc.RecordRead(env.ctx, sessionID, path)
	firstRead := env.svc.LastReadTime(env.ctx, sessionID, path)
	require.False(t, firstRead.IsZero())

	synctest.Test(t, func(t *testing.T) {
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		env.svc.RecordRead(env.ctx, sessionID, path)
		secondRead := env.svc.LastReadTime(env.ctx, sessionID, path)

		require.False(t, secondRead.Before(firstRead), "second read time should not be before first")
	})
}

func TestService_RecordRead_DifferentSessions(t *testing.T) {
	env := setupTest(t)

	path := "/shared/file.go"
	session1, session2 := "session-1", "session-2"
	env.createSession(t, session1)
	env.createSession(t, session2)

	env.svc.RecordRead(env.ctx, session1, path)

	lastRead1 := env.svc.LastReadTime(env.ctx, session1, path)
	require.False(t, lastRead1.IsZero())

	lastRead2 := env.svc.LastReadTime(env.ctx, session2, path)
	require.True(t, lastRead2.IsZero(), "session 2 should not see session 1's read")
}

func TestService_RecordRead_DifferentPaths(t *testing.T) {
	env := setupTest(t)

	sessionID := "test-session-3"
	path1, path2 := "/path/to/file1.go", "/path/to/file2.go"
	env.createSession(t, sessionID)

	env.svc.RecordRead(env.ctx, sessionID, path1)

	lastRead1 := env.svc.LastReadTime(env.ctx, sessionID, path1)
	require.False(t, lastRead1.IsZero())

	lastRead2 := env.svc.LastReadTime(env.ctx, sessionID, path2)
	require.True(t, lastRead2.IsZero(), "path2 should not be recorded")
}

func TestService_RecordWriteAndExec(t *testing.T) {
	t.Parallel()

	env := setupTest(t)

	sessionID := "test-session-events"
	env.createSession(t, sessionID)

	env.svc.RecordWrite(env.ctx, sessionID, "/path/to/file.go")
	env.svc.RecordExec(env.ctx, sessionID, "go test ./...")

	writes, err := env.svc.ListRecentWrites(env.ctx, sessionID, 10)
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Clean("/path/to/file.go")}, writePaths(writes),
		"only writes are listed, and the stored path is absolute")

	var events int
	require.NoError(t, env.conn.QueryRowContext(
		env.ctx, "SELECT COUNT(*) FROM file_events WHERE session_id = ?", sessionID,
	).Scan(&events))
	require.Equal(t, 2, events, "the exec record is stored alongside the write")
}

func TestService_RecordWriteRefreshesInsteadOfAppending(t *testing.T) {
	t.Parallel()

	env := setupTest(t)

	sessionID := "test-session-repeat"
	env.createSession(t, sessionID)

	env.svc.RecordWrite(env.ctx, sessionID, "/path/to/file.go")
	env.svc.RecordWrite(env.ctx, sessionID, "/path/to/file.go")

	writes, err := env.svc.ListRecentWrites(env.ctx, sessionID, 10)
	require.NoError(t, err)
	require.Len(t, writes, 1, "a repeated write is one ledger row")
}

func TestService_ListRecentWritesOrdersAndLimits(t *testing.T) {
	t.Parallel()

	env := setupTest(t)

	sessionID := "test-session-order"
	env.createSession(t, sessionID)

	// Same second for every write, which is what the real ledger sees for a
	// burst of edits: the path is the tiebreaker, so the order is stable.
	for _, path := range []string{"/w/c.go", "/w/a.go", "/w/b.go"} {
		env.svc.RecordWrite(env.ctx, sessionID, path)
	}

	writes, err := env.svc.ListRecentWrites(env.ctx, sessionID, 2)
	require.NoError(t, err)
	require.Len(t, writes, 2, "the limit is applied by the query")

	all, err := env.svc.ListRecentWrites(env.ctx, sessionID, 10)
	require.NoError(t, err)
	require.Equal(t, []string{"/w/a.go", "/w/b.go", "/w/c.go"}, writePaths(all),
		"same-second writes break the tie on path, so the order is stable")
}

func TestService_ListRecentWritesIsPerSession(t *testing.T) {
	t.Parallel()

	env := setupTest(t)

	env.createSession(t, "session-a")
	env.createSession(t, "session-b")

	env.svc.RecordWrite(env.ctx, "session-a", "/a/file.go")

	writes, err := env.svc.ListRecentWrites(env.ctx, "session-b", 10)
	require.NoError(t, err)
	require.Empty(t, writes, "another session's writes are not visible")
}

func TestService_ListRecentWritesZeroLimit(t *testing.T) {
	t.Parallel()

	env := setupTest(t)

	sessionID := "test-session-zero"
	env.createSession(t, sessionID)
	env.svc.RecordWrite(env.ctx, sessionID, "/path/file.go")

	writes, err := env.svc.ListRecentWrites(env.ctx, sessionID, 0)
	require.NoError(t, err)
	require.Empty(t, writes)
}

func TestService_RecordEventIgnoresEmptyInput(t *testing.T) {
	t.Parallel()

	env := setupTest(t)

	sessionID := "test-session-empty"
	env.createSession(t, sessionID)

	env.svc.RecordWrite(env.ctx, sessionID, "")
	env.svc.RecordExec(env.ctx, sessionID, "")
	env.svc.RecordWrite(env.ctx, "", "/path/file.go")

	var events int
	require.NoError(t, env.conn.QueryRowContext(
		env.ctx, "SELECT COUNT(*) FROM file_events",
	).Scan(&events))
	require.Zero(t, events, "an empty session or value is not a recordable event")
}

// writePaths extracts the paths from ledger entries, which is what the tests
// that predate the timestamp compare against.
func writePaths(writes []WriteEvent) []string {
	paths := make([]string, 0, len(writes))
	for _, write := range writes {
		paths = append(paths, write.Path)
	}
	return paths
}
