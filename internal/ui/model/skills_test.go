package model

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/charmbracelet/crush/internal/ui/common"
	uistyles "github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/ui/util"
	uv "github.com/charmbracelet/ultraviolet"
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

	items := ui.allSkillStatusItems()
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

// TestSkillStatusItemsDisabledFromSession verifies the disabled marker comes
// from the session in view, and that disabled skills stay listed so they can
// be re-enabled by clicking.
func TestSkillStatusItemsDisabledFromSession(t *testing.T) {
	t.Parallel()

	st := uistyles.CharmtonePantera()
	ui := &UI{
		com:     &common.Common{Styles: &st},
		session: &session.Session{ID: "s1", DisabledSkills: []string{"go-doc", "crush-config"}},
		skillStates: []*skills.SkillState{
			{Name: "go-doc", Path: "/tmp/go-doc/SKILL.md", State: skills.StateNormal},
			{Name: "keep-me", Path: "/tmp/keep-me/SKILL.md", State: skills.StateNormal},
		},
	}

	byName := make(map[string]skillStatusItem)
	for _, item := range ui.allSkillStatusItems() {
		byName[item.name] = item
	}

	require.True(t, byName["go-doc"].disabled)
	require.True(t, byName["crush-config"].disabled)
	require.False(t, byName["keep-me"].disabled)
}

// TestSkillStatusItemsDisabledFallsBackToGlobalConfig verifies the landing
// page (no session) still reflects the global default that new sessions are
// seeded from.
func TestSkillStatusItemsDisabledFallsBackToGlobalConfig(t *testing.T) {
	t.Parallel()

	st := uistyles.CharmtonePantera()
	ui := &UI{
		com: &common.Common{
			Styles:    &st,
			Workspace: &testWorkspace{cfg: &config.Config{Options: &config.Options{DisabledSkills: []string{"go-doc"}}}},
		},
		skillStates: []*skills.SkillState{
			{Name: "go-doc", Path: "/tmp/go-doc/SKILL.md", State: skills.StateNormal},
		},
	}

	var found bool
	for _, item := range ui.allSkillStatusItems() {
		if item.name == "go-doc" {
			require.True(t, item.disabled)
			found = true
		}
	}
	require.True(t, found)
}

func TestSkillInvocationSuffix(t *testing.T) {
	t.Parallel()

	require.Equal(t, "[u]", skillInvocationSuffix(true, true))
	require.Equal(t, "[m]", skillInvocationSuffix(false, false))
	require.Equal(t, "[u+m]", skillInvocationSuffix(true, false))
	require.Empty(t, skillInvocationSuffix(false, true))
}

// TestSkillStatusItemsGroupsByInvocation verifies the two-level sort: rows
// group by invocation marker first and sort alphabetically inside a group.
// Skills with no marker sort last.
func TestSkillStatusItemsGroupsByInvocation(t *testing.T) {
	t.Parallel()

	st := uistyles.CharmtonePantera()
	ui := &UI{
		com: &common.Common{Styles: &st},
		skillStates: []*skills.SkillState{
			{Name: "zebra-user", Path: "/tmp/zebra-user/SKILL.md", State: skills.StateNormal, UserInvocable: true, DisableModelInvocation: true},
			{Name: "beta-model", Path: "/tmp/beta-model/SKILL.md", State: skills.StateNormal},
			{Name: "alpha-both", Path: "/tmp/alpha-both/SKILL.md", State: skills.StateNormal, UserInvocable: true},
			{Name: "alpha-user", Path: "/tmp/alpha-user/SKILL.md", State: skills.StateNormal, UserInvocable: true, DisableModelInvocation: true},
			{Name: "quiet", Path: "/tmp/quiet/SKILL.md", State: skills.StateNormal, DisableModelInvocation: true},
			{Name: "broken", Path: "/tmp/broken/SKILL.md", State: skills.StateError},
		},
	}

	// Restrict the assertion to the injected states: builtin skills also
	// show up in the list and are covered by other tests.
	tracked := map[string]bool{
		"alpha-both": true,
		"alpha-user": true,
		"beta-model": true,
		"broken":     true,
		"quiet":      true,
		"zebra-user": true,
	}
	var got []string
	for _, item := range ui.allSkillStatusItems() {
		if tracked[item.name] {
			got = append(got, item.name+item.suffix)
		}
	}

	require.Equal(t, []string{
		"beta-model[m]",
		"alpha-both[u+m]",
		"alpha-user[u]",
		"zebra-user[u]",
		"broken",
		"quiet",
	}, got)
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
	for _, item := range ui.allSkillStatusItems() {
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

// TestSkillsInfoTruncatesLongRows guards against skill rows wrapping onto a
// second line: names too long for the column are elided and the invocation
// suffix is kept on the same line.
func TestSkillsInfoTruncatesLongRows(t *testing.T) {
	t.Parallel()

	const width = 30
	st := uistyles.CharmtonePantera()
	ui := &UI{
		com: &common.Common{Styles: &st},
		skillStates: []*skills.SkillState{
			{Name: "resolving-merge-conflicts", Path: "/tmp/resolving-merge-conflicts/SKILL.md", State: skills.StateNormal},
			{Name: "improve-codebase-architecture", Path: "/tmp/improve-codebase-architecture/SKILL.md", State: skills.StateNormal, UserInvocable: true},
		},
	}

	info := ansi.Strip(ui.skillsInfo(width, 50, false))

	for _, line := range strings.Split(info, "\n") {
		require.LessOrEqual(t, ansi.StringWidth(line), width, "skill rows must not wrap")
	}
	require.Contains(t, info, "resolving-merge-conflicts[m]")
	require.Contains(t, info, "improve-codebase-archi…[u+m]")
}

// TestSidebarSkillRowsMatchRenderedLines verifies each recorded clickable
// row points at the content line that actually renders that skill, which is
// what keeps click-to-toggle correct as the sections above change height.
func TestSidebarSkillRowsMatchRenderedLines(t *testing.T) {
	t.Parallel()

	u := buildSidebarBenchUI(t)
	u.height = 120
	u.updateLayoutAndSize()
	u.updateSidebarScrollState()

	require.NotEmpty(t, u.sidebarSkillRows)
	for _, row := range u.sidebarSkillRows {
		require.Less(t, row.Line, len(u.sidebarLines), "row line must be inside the content")
		require.GreaterOrEqual(t, row.Height, 1)
		line := ansi.Strip(u.sidebarLines[row.Line])
		require.Contains(t, line, row.Name, "row line must render its skill")
		require.True(t, strings.HasPrefix(line, "●"), "row must start with the status circle: %q", line)
	}
}

// skillToggleAt performs the press+release gesture the UI uses to toggle a
// skill and returns the resulting command.
func skillToggleAt(t *testing.T, u *UI, x, y int) tea.Cmd {
	t.Helper()
	require.True(t, u.handleSkillPress(tea.MouseClickMsg(uv.Mouse{
		Button: uv.MouseLeft,
		X:      x,
		Y:      y,
	})), "press must land on a skill indicator")
	cmd, handled := u.handleSkillRelease(tea.MouseReleaseMsg(uv.Mouse{
		Button: uv.MouseLeft,
		X:      x,
		Y:      y,
	}))
	require.True(t, handled, "release must complete the skill press")
	return cmd
}

// sidebarSkillHit returns the screen coordinate of a sidebar skill's
// indicator circle.
func sidebarSkillHit(u *UI, row skillRow) (int, int) {
	contentTop := u.layout.sidebar.Min.Y + lipgloss.Height(u.sidebarDrawLogo)
	return u.layout.sidebar.Min.X, contentTop + row.Line - u.sidebarOffset
}

// TestSkillRowSingleIndicator verifies each rendered row carries exactly one
// status circle: the enabled state is folded into that circle rather than
// drawn as a second checkbox.
func TestSkillRowSingleIndicator(t *testing.T) {
	t.Parallel()

	st := uistyles.CharmtonePantera()
	ui := &UI{
		com:     &common.Common{Styles: &st},
		session: &session.Session{ID: "s1", DisabledSkills: []string{"off"}},
		skillStates: []*skills.SkillState{
			{Name: "on", Path: "/tmp/on/SKILL.md", State: skills.StateNormal},
			{Name: "off", Path: "/tmp/off/SKILL.md", State: skills.StateNormal},
		},
	}

	byName := make(map[string]skillStatusItem)
	for _, item := range ui.allSkillStatusItems() {
		byName[item.name] = item
	}
	require.Equal(t, st.Resource.OnlineIcon.String(), byName["on"].icon)
	require.Equal(t, st.Resource.DisabledIcon.String(), byName["off"].icon)

	for _, name := range []string{"on", "off"} {
		item := byName[name]
		row := ansi.Strip(common.Status(&st, common.StatusOpts{
			Icon:  item.icon,
			Title: item.title(&st, 40),
		}, 40))
		require.Equal(t, 1, strings.Count(row, "●"), "row %q must render one circle: %q", name, row)
		require.NotContains(t, row, "◉")
		require.NotContains(t, row, "○")
	}
}

// TestHandleSidebarSkillClickTogglesSessionSkill presses the first skills
// indicator and verifies the release updates and persists the session's set
// through the workspace, without touching global config.
func TestHandleSidebarSkillClickTogglesSessionSkill(t *testing.T) {
	t.Parallel()

	u := buildSidebarBenchUI(t)
	u.height = 120
	u.updateLayoutAndSize()
	u.updateSidebarScrollState()

	require.NotEmpty(t, u.sidebarSkillRows)
	row := u.sidebarSkillRows[0]
	require.NotContains(t, u.session.DisabledSkills, row.Name)

	x, y := sidebarSkillHit(u, row)
	cmd := skillToggleAt(t, u, x, y)
	require.NotNil(t, cmd)
	require.Contains(t, u.session.DisabledSkills, row.Name, "release must disable the skill")
	msg, isSkillsSet := cmd().(sessionSkillsSetMsg)
	require.True(t, isSkillsSet)
	require.NoError(t, msg.err)
	require.IsType(t, util.InfoMsg{}, u.applySessionSkillsSet(msg)())

	ws, ok := u.com.Workspace.(*testWorkspace)
	require.True(t, ok)
	sessionID, disabled := ws.recordedDisabledSkills()
	require.Equal(t, u.session.ID, sessionID)
	require.Contains(t, disabled, row.Name)

	// Releasing on the same row again re-enables it. The debounce is cleared
	// as a later, deliberate click would clear it.
	u.updateSidebarScrollState()
	require.NotEmpty(t, u.sidebarSkillRows)
	u.lastSkillToggle = skillToggleState{}
	cmd = skillToggleAt(t, u, x, y)
	require.NotNil(t, cmd)
	require.NotContains(t, u.session.DisabledSkills, row.Name)
}

// TestSkillToggleRollsBackOnPersistFailure verifies a failed write restores
// the session's previous set, so the sidebar cannot keep showing a value the
// database rejected.
func TestSkillToggleRollsBackOnPersistFailure(t *testing.T) {
	t.Parallel()

	u := buildSidebarBenchUI(t)
	ws, ok := u.com.Workspace.(*testWorkspace)
	require.True(t, ok)
	ws.setSkillsErr = errors.New("write failed")

	u.height = 120
	u.updateLayoutAndSize()
	u.updateSidebarScrollState()
	require.NotEmpty(t, u.sidebarSkillRows)

	row := u.sidebarSkillRows[0]
	before := slices.Clone(u.session.DisabledSkills)
	x, y := sidebarSkillHit(u, row)

	cmd := skillToggleAt(t, u, x, y)
	require.NotNil(t, cmd)
	require.Contains(t, u.session.DisabledSkills, row.Name, "the optimistic update flips the row immediately")

	msg, isSkillsSet := cmd().(sessionSkillsSetMsg)
	require.True(t, isSkillsSet)
	require.Error(t, msg.err)

	require.NotNil(t, u.applySessionSkillsSet(msg))
	require.Equal(t, before, u.session.DisabledSkills, "a failed write must restore the previous set")
	require.Equal(t, skillToggleState{}, u.lastSkillToggle, "a failed toggle must not debounce a retry")
}

// TestHandleSkillPressIgnoresNonIndicators verifies only the leading circle
// is a hit target, so a stray click on the name, the logo, or a right-click
// is left to the normal focus handling.
func TestHandleSkillPressIgnoresNonIndicators(t *testing.T) {
	t.Parallel()

	u := buildSidebarBenchUI(t)
	u.height = 120
	u.updateLayoutAndSize()
	u.updateSidebarScrollState()
	require.NotEmpty(t, u.sidebarSkillRows)

	row := u.sidebarSkillRows[0]
	x, y := sidebarSkillHit(u, row)

	require.False(t, u.handleSkillPress(tea.MouseClickMsg(uv.Mouse{
		Button: uv.MouseLeft,
		X:      x,
		Y:      u.layout.sidebar.Min.Y, // logo area, above the rows
	})))
	require.False(t, u.handleSkillPress(tea.MouseClickMsg(uv.Mouse{
		Button: uv.MouseLeft,
		X:      x + 10, // the skill name, not its indicator
		Y:      y,
	})))
	require.False(t, u.handleSkillPress(tea.MouseClickMsg(uv.Mouse{
		Button: uv.MouseRight,
		X:      x,
		Y:      y,
	})))
}

// TestSkillToggleDebouncesDoubleClick verifies a second release inside the
// debounce window is ignored, so a double-click does not flip a skill back.
func TestSkillToggleDebouncesDoubleClick(t *testing.T) {
	t.Parallel()

	u := buildSidebarBenchUI(t)
	u.height = 120
	u.updateLayoutAndSize()
	u.updateSidebarScrollState()

	row := u.sidebarSkillRows[0]
	x, y := sidebarSkillHit(u, row)

	require.NotNil(t, skillToggleAt(t, u, x, y))
	require.Contains(t, u.session.DisabledSkills, row.Name)

	u.updateSidebarScrollState()
	require.Nil(t, skillToggleAt(t, u, x, y))
	require.Contains(t, u.session.DisabledSkills, row.Name, "double-click must not re-enable")
}

// TestSkillDragCancelsToggle verifies a press that turns into a drag never
// toggles the skill.
func TestSkillDragCancelsToggle(t *testing.T) {
	t.Parallel()

	u := buildSidebarBenchUI(t)
	u.height = 120
	u.updateLayoutAndSize()
	u.updateSidebarScrollState()

	row := u.sidebarSkillRows[0]
	x, y := sidebarSkillHit(u, row)

	require.True(t, u.handleSkillPress(tea.MouseClickMsg(uv.Mouse{Button: uv.MouseLeft, X: x, Y: y})))
	u.handleSkillMotion(tea.MouseMotionMsg(uv.Mouse{Button: uv.MouseLeft, X: x, Y: y + skillClickSlop + 1}))
	cmd, handled := u.handleSkillRelease(tea.MouseReleaseMsg(uv.Mouse{Button: uv.MouseLeft, X: x, Y: y + skillClickSlop + 1}))
	require.False(t, handled)
	require.Nil(t, cmd)
	require.NotContains(t, u.session.DisabledSkills, row.Name)
}

// newLandingTestUI builds a landing page model (no session) with a live
// workspace stub so its skill toggles can be exercised.
func newLandingTestUI(t *testing.T) *UI {
	t.Helper()

	u := newTestUI()
	u.com.Workspace = &testWorkspace{cfg: &config.Config{Options: &config.Options{}}}
	u.state = uiLanding
	u.session = nil
	u.width = 120
	u.height = 45
	u.updateLayoutAndSize()
	return u
}

// TestLandingSkillToggleStagesPending verifies the landing page can pick
// skills before a session exists, that nothing is persisted yet, and that
// the pending set is written onto the session it is applied to.
func TestLandingSkillToggleStagesPending(t *testing.T) {
	t.Parallel()

	u := newLandingTestUI(t)
	view := ansi.Strip(u.landingView())
	require.NotEmpty(t, u.landingSkillRows)

	row := u.landingSkillRows[0]
	x := u.landingSkillsRect.Min.X
	y := u.landingSkillsRect.Min.Y + row.Line

	// The recorded geometry must agree with what the view actually painted:
	// the indicator circle and the skill name sit on that line.
	lines := strings.Split(view, "\n")
	localY := y - u.layout.main.Min.Y
	localX := x - u.layout.main.Min.X
	require.Greater(t, len(lines), localY)
	line := []rune(lines[localY])
	require.Greater(t, len(line), localX)
	require.Equal(t, "●", string(line[localX]), "landing row must start with the status circle")
	require.Contains(t, lines[localY], row.Name)

	cmd := skillToggleAt(t, u, x, y)
	require.NotNil(t, cmd)
	require.True(t, u.pendingSkillsDirty)
	require.Contains(t, u.pendingDisabledSkills, row.Name)

	ws, ok := u.com.Workspace.(*testWorkspace)
	require.True(t, ok)
	_, disabled := ws.recordedDisabledSkills()
	require.Nil(t, disabled, "landing toggles must not be persisted before a session exists")

	applied, ok, err := u.applyPendingSkills(context.Background(), "new-session")
	require.NoError(t, err)
	require.True(t, ok)
	require.Contains(t, applied, row.Name)
	sessionID, persisted := ws.recordedDisabledSkills()
	require.Equal(t, "new-session", sessionID)
	require.Contains(t, persisted, row.Name)
	require.False(t, u.pendingSkillsDirty)
}

// TestApplyPendingSkillsKeepsPendingOnFailure verifies a failed hand-off
// leaves the pending set intact so the caller can abort instead of silently
// starting the session with the global default.
func TestApplyPendingSkillsKeepsPendingOnFailure(t *testing.T) {
	t.Parallel()

	u := newLandingTestUI(t)
	ws, ok := u.com.Workspace.(*testWorkspace)
	require.True(t, ok)
	ws.setSkillsErr = errors.New("write failed")

	u.pendingDisabledSkills = []string{"alpha"}
	u.pendingSkillsDirty = true

	_, pending, err := u.applyPendingSkills(context.Background(), "new-session")
	require.Error(t, err)
	require.True(t, pending)
	require.True(t, u.pendingSkillsDirty, "the pending set must survive a failed hand-off")
	require.Equal(t, []string{"alpha"}, u.pendingDisabledSkills)
}

// TestLandingSkillClickIgnoresNameArea verifies the landing page also only
// accepts the indicator circle as a hit target.
func TestLandingSkillClickIgnoresNameArea(t *testing.T) {
	t.Parallel()

	u := newLandingTestUI(t)
	_ = u.landingView()
	require.NotEmpty(t, u.landingSkillRows)

	row := u.landingSkillRows[0]
	require.False(t, u.handleSkillPress(tea.MouseClickMsg(uv.Mouse{
		Button: uv.MouseLeft,
		X:      u.landingSkillsRect.Min.X + 10,
		Y:      u.landingSkillsRect.Min.Y + row.Line,
	})))
}

// TestUserInvocableSkillsExcludesSessionDisabled verifies the '$'
// completions follow the session's disabled set.
func TestUserInvocableSkillsExcludesSessionDisabled(t *testing.T) {
	t.Parallel()

	u := newSkillTestUI()
	u.session = &session.Session{ID: "s1", DisabledSkills: []string{"grilling"}}

	values := u.userInvocableSkills()
	require.Empty(t, values)

	u.session.DisabledSkills = nil
	values = u.userInvocableSkills()
	require.Len(t, values, 1)
	require.Equal(t, "grilling", values[0].Name)

	// The catalog is the full discovered set so a session can re-enable a
	// skill the global default disabled.
	require.True(t, slices.ContainsFunc(u.skillCatalog, func(e skills.CatalogEntry) bool {
		return e.Name == "grilling"
	}))
}
