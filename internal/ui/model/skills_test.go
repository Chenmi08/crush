package model

import (
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/charmbracelet/crush/internal/ui/common"
	uistyles "github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// TestSkillStatusItemsIncludesBuiltinSkills verifies sidebar skills include
// both runtime-discovered skill states and builtin skills that may not have
// emitted a SkillState event yet.
func TestSkillStatusItemsIncludesBuiltinSkills(t *testing.T) {
	t.Parallel()

	st := uistyles.CharmtonePantera()
	ui := &UI{
		com: &common.Common{Styles: &st},
		skillStates: []*skills.SkillState{
			{Name: "go-doc", Path: "/tmp/go-doc/SKILL.md", State: skills.StateNormal},
		},
	}

	items := ui.skillStatusItems()
	require.NotEmpty(t, items)

	var hasGoDoc bool
	for _, item := range items {
		if item.name == "go-doc" {
			hasGoDoc = true
			break
		}
	}
	require.True(t, hasGoDoc)

	builtinSkills := skills.DiscoverBuiltin()
	require.NotEmpty(t, builtinSkills)

	var hasBuiltin bool
	for _, skill := range builtinSkills {
		if skill.Name == "go-doc" {
			continue
		}
		for _, item := range items {
			if item.name == skill.Name {
				hasBuiltin = true
				break
			}
		}
		if hasBuiltin {
			break
		}
	}
	require.True(t, hasBuiltin)
}

func TestSkillStatusItemsExcludesDisabledSkills(t *testing.T) {
	t.Parallel()

	st := uistyles.CharmtonePantera()
	ui := &UI{
		com: &common.Common{
			Styles:    &st,
			Workspace: &testWorkspace{cfg: &config.Config{Options: &config.Options{DisabledSkills: []string{"go-doc", "crush-config"}}}},
		},
		skillStates: []*skills.SkillState{
			{Name: "go-doc", Path: "/tmp/go-doc/SKILL.md", State: skills.StateNormal},
		},
	}

	items := ui.skillStatusItems()

	for _, item := range items {
		require.NotEqual(t, "go-doc", item.name)
		require.NotEqual(t, "crush-config", item.name)
	}
}

func TestSkillInvocationSuffix(t *testing.T) {
	t.Parallel()

	require.Equal(t, "[u]", skillInvocationSuffix(true, true))
	require.Equal(t, "[m]", skillInvocationSuffix(false, false))
	require.Equal(t, "[u+m]", skillInvocationSuffix(true, false))
	require.Empty(t, skillInvocationSuffix(false, true))
}

func TestSkillStatusItemsInvocationSuffix(t *testing.T) {
	t.Parallel()

	st := uistyles.CharmtonePantera()
	ui := &UI{
		com: &common.Common{Styles: &st},
		skillStates: []*skills.SkillState{
			{Name: "user-only", Path: "/tmp/user-only/SKILL.md", State: skills.StateNormal, UserInvocable: true, DisableModelInvocation: true},
			{Name: "model-only", Path: "/tmp/model-only/SKILL.md", State: skills.StateNormal},
			{Name: "both", Path: "/tmp/both/SKILL.md", State: skills.StateNormal, UserInvocable: true},
			{Name: "broken", Path: "/tmp/broken/SKILL.md", State: skills.StateError},
		},
	}

	byName := make(map[string]skillStatusItem)
	for _, item := range ui.skillStatusItems() {
		byName[item.name] = item
	}

	require.Equal(t, "[u]", byName["user-only"].suffix)
	require.Equal(t, "[m]", byName["model-only"].suffix)
	require.Equal(t, "[u+m]", byName["both"].suffix)
	require.Empty(t, byName["broken"].suffix)

	info := ansi.Strip(ui.skillsInfo(80, 50, false))
	require.Contains(t, info, "user-only[u]")
	require.Contains(t, info, "model-only[m]")
	require.Contains(t, info, "both[u+m]")
}

// TestSkillsInfoTruncatesLongRows guards against skill rows wrapping onto
// a second line: names too long for the column are elided and the
// invocation suffix is kept on the same line.
func TestSkillsInfoTruncatesLongRows(t *testing.T) {
	t.Parallel()

	st := uistyles.CharmtonePantera()
	ui := &UI{
		com: &common.Common{Styles: &st},
		skillStates: []*skills.SkillState{
			{Name: "resolving-merge-conflicts", Path: "/tmp/resolving-merge-conflicts/SKILL.md", State: skills.StateNormal},
			{Name: "improve-codebase-architecture", Path: "/tmp/improve-codebase-architecture/SKILL.md", State: skills.StateNormal, UserInvocable: true},
		},
	}

	info := ansi.Strip(ui.skillsInfo(30, 50, false))

	// Exactly 30 cells: the whole row still fits.
	require.Contains(t, info, "resolving-merge-conflicts[m]")
	// 34 cells: the name is elided, the [u+m] suffix stays on the same line.
	require.Contains(t, info, "improve-codebase-archi…[u+m]")
}
