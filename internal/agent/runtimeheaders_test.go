package agent

import (
	"testing"

	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

func TestSessionHeaders_RuntimeVars(t *testing.T) {
	t.Parallel()

	headers := sessionHeaders(Model{
		RuntimeHeaders: map[string]string{
			"x-session":      "{{CRUSH_SESSION_ID}}",
			"x-session-hash": "{{CRUSH_SESSION_HASH}}",
			"x-message":      "{{CRUSH_MESSAGE_ID}}",
			"x-project":      "{{CRUSH_PROJECT_ID}}",
			"x-client":       "crush",
		},
		ProjectID: "proj",
	}, "sess-id", "msg-id")

	hash := session.HashID("sess-id")
	require.Equal(t, hash, headers["x-session-id"])
	require.Equal(t, hash, headers["x-session-affinity"])
	require.Equal(t, "sess-id", headers["x-session"])
	require.Equal(t, hash, headers["x-session-hash"])
	require.Equal(t, "msg-id", headers["x-message"])
	require.Equal(t, "proj", headers["x-project"])
	require.Equal(t, "crush", headers["x-client"])
}

func TestSessionHeaders_EmptyMessageID(t *testing.T) {
	t.Parallel()

	headers := sessionHeaders(Model{
		RuntimeHeaders: map[string]string{"x-message": "{{CRUSH_MESSAGE_ID}}"},
	}, "sess-id", "")
	_, ok := headers["x-message"]
	require.False(t, ok)
}
