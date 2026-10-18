package model

import (
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/sandbox"
	"github.com/charmbracelet/crush/internal/ui/common"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/attachments"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// leadingSpaces counts the spaces at the start of a stripped line.
func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

// drawStatusLines draws the status bar into a screen buffer and returns the
// visible content of each row.
func drawStatusLines(t *testing.T, st *Status, w, h int) []string {
	t.Helper()
	scr := uv.NewScreenBuffer(w, h)
	st.Draw(scr, uv.Rect(0, 0, w, h))
	lines := strings.Split(ansi.Strip(scr.Render()), "\n")
	return lines
}

func TestStatusDrawExpandedHelpRowsAlignWithBadge(t *testing.T) {
	t.Parallel()

	u := newPrismTestUI()
	u.attachments = attachments.New(nil, attachments.Keymap{})
	st := u.status
	st.helpKm = u
	st.SetWidth(100)
	st.ToggleHelp()
	st.SetMode(uiInputModePlan, false)

	lines := drawStatusLines(t, st, 100, 6)
	require.True(t, strings.HasPrefix(lines[0], strings.Repeat(" ", badgeLeftInset)+" "+"PLAN MODE"),
		"the badge row must start with the badge inset and its padding: %q", lines[0])

	// Every subsequent help row must start at the same column as the hints
	// on the badge row: badge inset + badge width + separator + help padding.
	badge := st.modeBadge()
	wantHintsCol := badgeLeftInset + lipgloss.Width(badge) + 1 + u.com.Styles.Status.Help.GetPaddingLeft()
	for i, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			break
		}
		require.Equal(t, wantHintsCol, leadingSpaces(line),
			"expanded help row %d must align with the badge row hints", i+1)
	}
}

func TestStatusDrawExpandedHelpRowsAlignWithoutBadge(t *testing.T) {
	t.Parallel()

	u := newPrismTestUI()
	u.attachments = attachments.New(nil, attachments.Keymap{})
	st := u.status
	st.helpKm = u
	st.SetWidth(100)
	st.ToggleHelp()
	st.SetMode(uiInputModeCode, false)

	lines := drawStatusLines(t, st, 100, 6)
	wantCol := u.com.Styles.Status.Help.GetPaddingLeft()
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			break
		}
		require.Equal(t, wantCol, leadingSpaces(line),
			"expanded help row %d must align with the first row", i)
	}
}

// newSandboxTestStatus builds a status bar with the default styles and a
// nil key map, which is all the sandbox badge needs.
func newSandboxTestStatus(t *testing.T) *Status {
	t.Helper()
	com := common.DefaultCommon(nil)
	return NewStatus(com, nil)
}

// TestSandboxBadge_DistinguishesEnforcement pins the requirement that the
// sandbox indicator distinguishes enforced and off states by style, while
// the label itself stays the bare mode name.
func TestSandboxBadge_DistinguishesEnforcement(t *testing.T) {
	t.Parallel()
	st := newSandboxTestStatus(t)

	st.SetSandbox("workspace-write", "full")
	full := st.SandboxBadge()
	require.Contains(t, full, "WORKSPACE-WRITE")
	require.NotContains(t, full, "PARTIAL")
	require.NotContains(t, full, "UNENFORCED")

	st.SetSandbox("workspace-write", "partial")
	partial := st.SandboxBadge()
	require.Contains(t, partial, "WORKSPACE-WRITE")
	require.NotContains(t, partial, "PARTIAL")
	// Full and partial render differently but carry the same label.
	require.NotEqual(t, full, partial)

	st.SetSandbox("danger-full-access", "none")
	off := st.SandboxBadge()
	require.Contains(t, off, "DANGER-FULL-ACCESS")
	require.NotContains(t, off, "UNENFORCED")
	require.NotEqual(t, partial, off)

	st.SetSandbox("", "")
	require.Empty(t, st.SandboxBadge())
}

// TestSandboxBadge_FromConfig covers the mode mapping the UI uses.
func TestSandboxBadge_FromConfig(t *testing.T) {
	t.Parallel()
	st := newSandboxTestStatus(t)

	st.SetSandboxFromConfig(nil)
	require.Empty(t, st.SandboxBadge(), "no config means no sandbox indicator")

	cfg := &config.Config{Permissions: &config.Permissions{Sandbox: &config.Sandbox{Mode: "read-only"}}}
	st.SetSandboxFromConfig(cfg)
	require.Contains(t, st.SandboxBadge(), "READ-ONLY")

	cfg.Permissions.Sandbox.Mode = "danger-full-access"
	st.SetSandboxFromConfig(cfg)
	require.Contains(t, st.SandboxBadge(), "DANGER-FULL-ACCESS")
	require.NotContains(t, st.SandboxBadge(), "PARTIAL")
	require.NotContains(t, st.SandboxBadge(), "UNENFORCED")

	// An unset mode defaults to workspace-write, never to danger access.
	cfg.Permissions.Sandbox.Mode = ""
	st.SetSandboxFromConfig(cfg)
	require.Contains(t, st.SandboxBadge(), "WORKSPACE-WRITE")
	require.NotContains(t, st.SandboxBadge(), "PARTIAL")
}

// TestSandboxDenialMsg_Notice pins the transient notice contract: a refusal
// is a short warning that fits a status line, a repeat announces the count,
// and nothing here is a question — authorization stays model-initiated.
func TestSandboxDenialMsg_Notice(t *testing.T) {
	t.Parallel()

	first := app.SandboxDenialMsg{DenialNotice: sandbox.DenialNotice{
		Tool: "write", Path: "/etc/hosts", Mode: "workspace-write", Count: 1,
	}}.String()
	require.Contains(t, first, "/etc/hosts")
	require.Contains(t, first, "sandbox denied")
	require.NotContains(t, first, "refusals this session")
	// Keep it short: it renders for a few seconds in a status bar.
	require.LessOrEqual(t, len(first), 80)

	repeat := app.SandboxDenialMsg{DenialNotice: sandbox.DenialNotice{
		Path: "/etc/hosts", Count: 4,
	}}.String()
	require.Contains(t, repeat, "(4 refusals this session)")

	// A protected-path refusal names the reason, so the user knows no
	// setting will re-allow it.
	protected := app.SandboxDenialMsg{DenialNotice: sandbox.DenialNotice{
		Path: "/w/crushrc", Protected: true, Count: 2,
	}}.String()
	require.Contains(t, protected, "protected path")
}

// TestSandboxSwitch_ConfigSnapshot pins the badge update path: after a mode
// switch the status badge reflects the new mode even before the next config
// snapshot arrives.
func TestSandboxSwitch_ConfigSnapshot(t *testing.T) {
	t.Parallel()
	st := newSandboxTestStatus(t)

	cfg := &config.Config{Permissions: &config.Permissions{Sandbox: &config.Sandbox{Mode: "read-only"}}}
	st.SetSandboxFromConfig(cfg)
	require.Contains(t, st.SandboxBadge(), "READ-ONLY")

	// The switch handler builds a snapshot with the resolved mode.
	after := &config.Config{Permissions: &config.Permissions{Sandbox: &config.Sandbox{Mode: "workspace-write"}}}
	st.SetSandboxFromConfig(after)
	require.Contains(t, st.SandboxBadge(), "WORKSPACE-WRITE")
}

// TestSandboxBadge_NoConfigStillShowsDefault pins that a workspace with no
// sandbox block still shows the badge. The sandbox is on by default, so
// hiding the indicator when the config has no explicit block meant the mode a
// user is trusting was invisible on a default install.
func TestSandboxBadge_NoConfigStillShowsDefault(t *testing.T) {
	t.Parallel()
	st := newSandboxTestStatus(t)

	st.SetSandboxFromConfig(&config.Config{})
	require.Contains(t, st.SandboxBadge(), "WORKSPACE-WRITE")

	st.SetSandboxFromConfig(&config.Config{Permissions: &config.Permissions{}})
	require.Contains(t, st.SandboxBadge(), "WORKSPACE-WRITE")
}

// TestSandboxBadge_ConfinedModeIsNotPartial pins that a healthy confined mode
// does not render as "partial": the status bar does not know the backend's
// enforcement fact, so claiming partial made every host look degraded.
func TestSandboxBadge_ConfinedModeIsNotPartial(t *testing.T) {
	t.Parallel()
	st := newSandboxTestStatus(t)
	st.SetSandboxFromConfig(&config.Config{Permissions: &config.Permissions{
		Sandbox: &config.Sandbox{Mode: "workspace-write"},
	}})
	require.Equal(t, "full", st.sandboxLevel)
}
