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

// TestDisabledSkillsArePerSession verifies each session persists its own
// skill opt-out set independently, which is what makes the sidebar toggle
// per-session rather than global.
func TestDisabledSkillsArePerSession(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := NewService(db.New(conn), conn)

	first, err := sessions.Create(t.Context(), "first")
	require.NoError(t, err)
	second, err := sessions.Create(t.Context(), "second")
	require.NoError(t, err)

	// Unsorted input with duplicates is normalized before persisting.
	require.NoError(t, sessions.SetDisabledSkills(t.Context(), first.ID, []string{"zeta", "alpha", "zeta"}))

	firstFetched, err := sessions.Get(t.Context(), first.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"alpha", "zeta"}, firstFetched.DisabledSkills)

	secondFetched, err := sessions.Get(t.Context(), second.ID)
	require.NoError(t, err)
	require.Empty(t, secondFetched.DisabledSkills, "other sessions must be unaffected")

	// Clearing the set persists as empty, not as the previous value.
	require.NoError(t, sessions.SetDisabledSkills(t.Context(), first.ID, nil))
	firstFetched, err = sessions.Get(t.Context(), first.ID)
	require.NoError(t, err)
	require.Empty(t, firstFetched.DisabledSkills)
}

// TestLegacySessionInheritsGlobalDisabledSkills verifies a session row that
// never stored its own opt-outs (written before per-session skills existed)
// keeps inheriting the global options.disabled_skills default instead of
// silently re-enabling every globally disabled skill.
func TestLegacySessionInheritsGlobalDisabledSkills(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	globalDefault := []string{"crush-config"}
	sessions := NewService(db.New(conn), conn, WithDefaultDisabledSkills(func() []string {
		return globalDefault
	}))

	// Create leaves disabled_skills NULL, which is exactly the shape of a row
	// written by an older binary.
	legacy, err := sessions.Create(t.Context(), "legacy")
	require.NoError(t, err)

	fetched, err := sessions.Get(t.Context(), legacy.ID)
	require.NoError(t, err)
	require.Equal(t, globalDefault, fetched.DisabledSkills)
}

// TestExplicitlyEmptyDisabledSkillsDoNotInheritDefault verifies an explicit
// "nothing disabled" choice survives the round trip and is distinguishable
// from a legacy row that never chose.
func TestExplicitlyEmptyDisabledSkillsDoNotInheritDefault(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	globalDefault := []string{"crush-config"}
	sessions := NewService(db.New(conn), conn, WithDefaultDisabledSkills(func() []string {
		return globalDefault
	}))

	created, err := sessions.Create(t.Context(), "cleared")
	require.NoError(t, err)

	// Clearing every opt-out must not fall back to the global default.
	require.NoError(t, sessions.SetDisabledSkills(t.Context(), created.ID, nil))
	fetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.Empty(t, fetched.DisabledSkills)
	require.NotNil(t, fetched.DisabledSkills, "an explicit empty set must not read as unset")
}
