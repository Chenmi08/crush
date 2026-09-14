package session

import (
	"testing"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/stretchr/testify/require"
)

func TestEstimatedUsageStateSurvivesFetchModifySave(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := NewService(db.New(conn), conn)

	created, err := sessions.Create(t.Context(), "test")
	require.NoError(t, err)
	created.PromptTokens = 100
	created.CompletionTokens = 50
	created.EstimatedUsage = true

	saved, err := sessions.Save(t.Context(), created)
	require.NoError(t, err)
	require.True(t, saved.EstimatedUsage)

	fetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.True(t, fetched.EstimatedUsage)

	fetched.Todos = []Todo{{
		Content:    "Check estimate state",
		Status:     TodoStatusInProgress,
		ActiveForm: "Checking estimate state",
	}}

	updated, err := sessions.Save(t.Context(), fetched)
	require.NoError(t, err)
	require.True(t, updated.EstimatedUsage)

	refetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.True(t, refetched.EstimatedUsage)
}

func TestEstimatedUsageStateCanBeClearedByExplicitSave(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := NewService(db.New(conn), conn)

	created, err := sessions.Create(t.Context(), "test")
	require.NoError(t, err)
	created.PromptTokens = 100
	created.CompletionTokens = 50
	created.EstimatedUsage = true

	saved, err := sessions.Save(t.Context(), created)
	require.NoError(t, err)
	require.True(t, saved.EstimatedUsage)

	saved.EstimatedUsage = false
	updated, err := sessions.Save(t.Context(), saved)
	require.NoError(t, err)
	require.False(t, updated.EstimatedUsage)

	refetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.False(t, refetched.EstimatedUsage)
}

func TestSessionTokensMetrics(t *testing.T) {
	t.Parallel()

	tokens := SessionTokens{
		InputTokens:         1000,
		OutputTokens:        500,
		CacheReadTokens:     3000,
		CacheCreationTokens: 1000,
	}
	require.Equal(t, int64(5000), tokens.TotalPromptTokens())
	require.InDelta(t, 0.6, tokens.CacheHitRate(), 0.0001)

	require.Zero(t, SessionTokens{}.TotalPromptTokens())
	require.Zero(t, SessionTokens{}.CacheHitRate())
}

func TestSessionTokensSurviveFetchModifySave(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := NewService(db.New(conn), conn)

	created, err := sessions.Create(t.Context(), "test")
	require.NoError(t, err)

	want := SessionTokens{
		InputTokens:         1500,
		OutputTokens:        2300,
		CacheReadTokens:     40_000,
		CacheCreationTokens: 800,
	}
	created.Totals = want

	saved, err := sessions.Save(t.Context(), created)
	require.NoError(t, err)
	require.Equal(t, want, saved.Totals)

	fetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.Equal(t, want, fetched.Totals)

	// A fetch-modify-save cycle for unrelated fields must preserve the
	// persisted totals.
	fetched.Todos = []Todo{{
		Content:    "Check session totals",
		Status:     TodoStatusInProgress,
		ActiveForm: "Checking session totals",
	}}
	updated, err := sessions.Save(t.Context(), fetched)
	require.NoError(t, err)
	require.Equal(t, want, updated.Totals)

	refetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.Equal(t, want, refetched.Totals)
}

func TestUpdateTitleAndUsageAccumulatesTotals(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := NewService(db.New(conn), conn)

	created, err := sessions.Create(t.Context(), "test")
	require.NoError(t, err)

	tokens := SessionTokens{
		InputTokens:         100,
		OutputTokens:        50,
		CacheReadTokens:     900,
		CacheCreationTokens: 20,
	}
	require.NoError(t, sessions.UpdateTitleAndUsage(t.Context(), created.ID, "New title", tokens, 0.5))

	fetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.Equal(t, "New title", fetched.Title)
	require.Equal(t, tokens, fetched.Totals)
	require.Equal(t, int64(120), fetched.PromptTokens)
	require.Equal(t, int64(50), fetched.CompletionTokens)
	require.Equal(t, 0.5, fetched.Cost)
}
