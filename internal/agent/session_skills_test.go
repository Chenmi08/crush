package agent

import (
	"testing"

	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/stretchr/testify/require"
)

// TestSessionActiveSkillsPerSession verifies skill discovery stays
// workspace-wide while visibility is filtered per session, so toggling a
// skill changes only the session it was toggled in.
func TestSessionActiveSkillsPerSession(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	ctx := t.Context()

	first, err := env.sessions.Create(ctx, "first")
	require.NoError(t, err)
	second, err := env.sessions.Create(ctx, "second")
	require.NoError(t, err)
	require.NoError(t, env.sessions.SetDisabledSkills(ctx, first.ID, []string{"beta"}))

	c := &coordinator{
		sessions: env.sessions,
		allSkills: []*skills.Skill{
			{Name: "alpha"},
			{Name: "beta"},
		},
	}

	firstActive, err := c.sessionActiveSkills(ctx, first.ID)
	require.NoError(t, err)
	require.Len(t, firstActive, 1)
	require.Equal(t, "alpha", firstActive[0].Name)

	secondActive, err := c.sessionActiveSkills(ctx, second.ID)
	require.NoError(t, err)
	require.Len(t, secondActive, 2, "other sessions keep every skill")

	// An unresolvable session reports the failure instead of silently
	// claiming every discovered skill is active.
	_, err = c.sessionActiveSkills(ctx, "missing")
	require.Error(t, err)
}

// TestSessionSkillsBlock verifies the block appended to a session's system
// prompt reflects that session's opt-outs and stays empty for sub-agents.
func TestSessionSkillsBlock(t *testing.T) {
	t.Parallel()

	all := []*skills.Skill{{Name: "alpha"}, {Name: "beta"}}

	// No opt-outs: every skill is advertised.
	block := sessionSkillsBlock(all, session.Session{}, false)
	require.Contains(t, block, "<name>alpha</name>")
	require.Contains(t, block, "<name>beta</name>")

	// The session's own opt-outs hide a skill only for that session.
	block = sessionSkillsBlock(all, session.Session{DisabledSkills: []string{"beta"}}, false)
	require.Contains(t, block, "<name>alpha</name>")
	require.NotContains(t, block, "<name>beta</name>")

	// Nothing visible means no block at all.
	require.Empty(t, sessionSkillsBlock(all, session.Session{DisabledSkills: []string{"alpha", "beta"}}, false))

	// Sub-agents never carry the catalog.
	require.Empty(t, sessionSkillsBlock(all, session.Session{}, true))
}
