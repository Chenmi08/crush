package model

import (
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
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
// sandbox indicator distinguishes enforced, degraded, and off, so a partial
// backend is never mistaken for a full one.
func TestSandboxBadge_DistinguishesEnforcement(t *testing.T) {
	t.Parallel()
	st := newSandboxTestStatus(t)

	st.SetSandbox("workspace-write", "full")
	full := st.SandboxBadge()
	require.Contains(t, full, "SANDBOX WORKSPACE-WRITE")
	require.NotContains(t, full, "PARTIAL")

	st.SetSandbox("workspace-write", "partial")
	partial := st.SandboxBadge()
	require.Contains(t, partial, "PARTIAL")
	require.NotEqual(t, full, partial)

	st.SetSandbox("danger-full-access", "none")
	off := st.SandboxBadge()
	require.Contains(t, off, "UNENFORCED")
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
	require.Contains(t, st.SandboxBadge(), "SANDBOX READ-ONLY")

	cfg.Permissions.Sandbox.Mode = "danger-full-access"
	st.SetSandboxFromConfig(cfg)
	require.Contains(t, st.SandboxBadge(), "UNENFORCED")

	// An unset mode defaults to workspace-write, never to danger access.
	cfg.Permissions.Sandbox.Mode = ""
	st.SetSandboxFromConfig(cfg)
	require.Contains(t, st.SandboxBadge(), "SANDBOX WORKSPACE-WRITE")
}
