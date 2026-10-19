package dialog

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func newTestPermissions(t *testing.T) *Permissions {
	t.Helper()
	s := styles.CharmtonePantera()
	com := &common.Common{Styles: &s}
	perm := permission.PermissionRequest{
		ID:         "perm-test",
		ToolCallID: "tool-call-test",
		ToolName:   "bash",
	}
	return NewPermissions(com, perm)
}

// TestPermissions_ActionKeysResolve verifies that action keys produce the
// correct permission response.
func TestPermissions_ActionKeysResolve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key    tea.KeyPressMsg
		action PermissionAction
	}{
		{keyMsg('a'), PermissionAllow},
		{keyMsg('A'), PermissionAllow},
		{keyMsg('d'), PermissionDeny},
		{keyMsg('D'), PermissionDeny},
		{keyMsg('s'), PermissionAllowForSession},
		{keyMsg('S'), PermissionAllowForSession},
	}

	for _, tc := range tests {
		p := newTestPermissions(t)
		action := p.HandleMsg(tc.key)
		resp, ok := action.(ActionPermissionResponse)
		require.Truef(t, ok, "key %q should produce ActionPermissionResponse", tc.key.Text)
		require.Equal(t, tc.action, resp.Action)
	}
}

// TestPermissions_NavigationCyclesOptions verifies that tab and arrow keys
// cycle through the three permission options.
func TestPermissions_NavigationCyclesOptions(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	require.Equal(t, 0, p.selectedOption)

	// Tab cycles forward.
	p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, 1, p.selectedOption)

	p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, 2, p.selectedOption)

	// Wrap around.
	p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, 0, p.selectedOption)

	// Left cycles backward.
	p.HandleMsg(keyMsg('h'))
	require.Equal(t, 2, p.selectedOption)
}

// TestPermissions_EnterConfirmsSelection verifies that enter confirms the
// currently selected option.
func TestPermissions_EnterConfirmsSelection(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	p.selectedOption = 1 // Allow for session.

	action := p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	resp, ok := action.(ActionPermissionResponse)
	require.True(t, ok)
	require.Equal(t, PermissionAllowForSession, resp.Action)
}

// TestPermissions_EscapeDenies verifies that escape denies the request.
func TestPermissions_EscapeDenies(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	action := p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape})
	resp, ok := action.(ActionPermissionResponse)
	require.True(t, ok)
	require.Equal(t, PermissionDeny, resp.Action)
}

// TestPermissions_EscalationRendersReasoning verifies the Leave Sandbox
// dialog shows the model's explanation of why it needs to leave, and omits
// the block entirely when the request carries none.
func TestPermissions_EscalationRendersReasoning(t *testing.T) {
	t.Parallel()

	s := styles.CharmtonePantera()
	com := &common.Common{Styles: &s}

	t.Run("with reasoning", func(t *testing.T) {
		t.Parallel()
		perm := permission.PermissionRequest{
			ID:             "perm-test",
			ToolName:       "bash",
			Escalation:     true,
			EscalationMode: "danger-full-access",
			Reasoning:      "Delete the report cache outside the workspace; no workspace path applies.",
		}
		p := NewPermissions(com, perm)

		view := p.renderHeader(100)
		require.Contains(t, view, "Leave Sandbox?")
		require.Contains(t, view, "Why")
		require.Contains(t, view, "Delete the report cache")
		// Inline layout: the reasoning follows on the same line as the
		// Why key, like every other Key/value row in the dialog.
		require.Regexp(t, `Why\s+Delete the report cache`, ansi.Strip(view))
	})

	t.Run("without reasoning", func(t *testing.T) {
		t.Parallel()
		perm := permission.PermissionRequest{
			ID:             "perm-test",
			ToolName:       "bash",
			Escalation:     true,
			EscalationMode: "danger-full-access",
		}
		p := NewPermissions(com, perm)

		view := p.renderHeader(100)
		require.Contains(t, view, "Leave Sandbox?")
		require.NotContains(t, view, "Why")
	})
}

// permissionsTestWorkspace satisfies the Workspace dependency the bash
// content panel consults for the working-directory prefix it strips.
type permissionsTestWorkspace struct {
	workspace.Workspace
}

func (w *permissionsTestWorkspace) WorkingDir() string { return "/tmp/project" }

// TestPermissions_EscalationShowsToolContent verifies the Leave Sandbox
// dialog shows what the call will actually do — the command for bash, the
// file path and diff for the file tools — instead of the escalation request
// object the request carried before. An approval that leaves the sandbox is
// the highest-stakes prompt in the UI; it must not be blind.
func TestPermissions_EscalationShowsToolContent(t *testing.T) {
	t.Parallel()

	s := styles.CharmtonePantera()
	com := &common.Common{Styles: &s, Workspace: &permissionsTestWorkspace{}}

	t.Run("bash renders the command panel", func(t *testing.T) {
		t.Parallel()
		perm := permission.PermissionRequest{
			ID:             "perm-test",
			ToolName:       tools.BashToolName,
			Escalation:     true,
			EscalationMode: "danger-full-access",
			Reasoning:      "Need to write the report outside the workspace.",
			Params: tools.BashPermissionsParams{
				Description: "escalated write",
				Command:     "rm -rf /var/lib/report-cache",
			},
		}
		p := NewPermissions(com, perm)

		header := ansi.Strip(p.renderHeader(100))
		require.Contains(t, header, "Leave Sandbox?")
		require.Contains(t, header, "Why")

		content := ansi.Strip(p.renderContent(100))
		require.Contains(t, content, "rm -rf /var/lib/report-cache",
			"the escalated command must be visible before approval")
	})

	t.Run("write renders the file diff", func(t *testing.T) {
		t.Parallel()
		perm := permission.PermissionRequest{
			ID:             "perm-test",
			ToolName:       tools.WriteToolName,
			Escalation:     true,
			EscalationMode: "workspace-write",
			Reasoning:      "Writing outside the workspace once.",
			Params: tools.WritePermissionsParams{
				FilePath:   "/tmp/project/notes.txt",
				NewContent: "hello",
			},
		}
		p := NewPermissions(com, perm)

		header := ansi.Strip(p.renderHeader(100))
		require.Contains(t, header, "Leave Sandbox?")

		// Draw marks the viewport dirty on first render because its width
		// changes from zero; the direct renderContent call must do the same
		// or renderDiff serves its empty cache.
		p.viewportDirty = true
		content := ansi.Strip(p.renderContent(100))
		require.Contains(t, content, "@@ -1,0 +1,1 @@",
			"the escalated write must render as a diff")
		require.Contains(t, content, "+ hello",
			"the escalated write's content must be visible before approval")
	})
}

// TestPermissions_EscalationHasNoSessionOption verifies an escalation
// (Leave Sandbox?) cannot offer "Allow for Session": the permission service
// deliberately does not record escalations as session grants, so the button
// would promise something it never honors.
func TestPermissions_EscalationHasNoSessionOption(t *testing.T) {
	t.Parallel()

	s := styles.CharmtonePantera()
	com := &common.Common{Styles: &s}
	perm := permission.PermissionRequest{
		ID:         "perm-escalation",
		ToolCallID: "tool-call-escalation",
		ToolName:   tools.WriteToolName,
		Escalation: true,
	}
	p := NewPermissions(com, perm)
	require.Equal(t, 2, p.optionCount())
	require.False(t, p.canGrantForSession())

	// Tab cycles Allow -> Deny -> Allow, never landing on a session grant.
	p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, 1, p.selectedOption)
	p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, 0, p.selectedOption)

	// The session key is a no-op: the dialog stays open.
	require.Nil(t, p.HandleMsg(keyMsg('s')))

	// Confirming the second option denies, which is what it renders as.
	p.selectedOption = 1
	action := p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	resp, ok := action.(ActionPermissionResponse)
	require.True(t, ok)
	require.Equal(t, PermissionDeny, resp.Action)
}
