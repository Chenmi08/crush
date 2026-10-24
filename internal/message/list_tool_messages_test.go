package message

import (
	"testing"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

// ListToolMessages backs `recall`: it must return tool rows only, oldest
// first, and it must not be bounded by the summary anchor — the whole point
// is that archived results stay reachable after compaction.
func TestListToolMessages(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t)

	mk := func(role MessageRole, parts ...ContentPart) Message {
		t.Helper()

		msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{
			Role:  role,
			Parts: parts,
		})
		require.NoError(t, err)
		return msg
	}

	first := mk(Tool, ToolResult{ToolCallID: "call_1", Name: "bash", Content: "one"})
	mk(User, TextContent{Text: "a user message"})
	mk(Assistant, TextContent{Text: "an assistant message"})
	second := mk(Tool, ToolResult{ToolCallID: "call_2", Name: "view", Content: "two"})

	got, err := svc.ListToolMessages(t.Context(), sessionID)
	require.NoError(t, err)
	require.Len(t, got, 2, "only tool-role messages belong in the result")

	ids := []string{got[0].ID, got[1].ID}
	require.Contains(t, ids, first.ID)
	require.Contains(t, ids, second.ID)

	for _, msg := range got {
		require.Equal(t, Tool, msg.Role)
		require.NotEmpty(t, msg.ToolResults())
	}

	// Ascending by created_at, which has one-second resolution: rows
	// written in the same second tie legitimately, so assert the
	// non-decreasing property rather than an exact order.
	require.LessOrEqual(t, got[0].CreatedAt, got[1].CreatedAt)
}

func TestListToolMessages_SessionIsolation(t *testing.T) {
	t.Parallel()

	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	q := db.New(conn)
	sessions := session.NewService(q, conn)
	svc := NewService(q)

	own, err := sessions.Create(t.Context(), "own")
	require.NoError(t, err)
	other, err := sessions.Create(t.Context(), "other")
	require.NoError(t, err)

	for _, tc := range []struct{ sessionID, callID string }{
		{own.ID, "call_own"},
		{other.ID, "call_other"},
	} {
		_, err := svc.Create(t.Context(), tc.sessionID, CreateMessageParams{
			Role:  Tool,
			Parts: []ContentPart{ToolResult{ToolCallID: tc.callID, Name: "bash", Content: tc.callID}},
		})
		require.NoError(t, err)
	}

	got, err := svc.ListToolMessages(t.Context(), own.ID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "call_own", got[0].ToolResults()[0].ToolCallID)
}

// A tool result stays readable after the session has been compacted: the
// query takes no summary anchor, so nothing can bound it to the tail.
func TestListToolMessages_IgnoresSummaryAnchor(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t)

	archived, err := svc.Create(t.Context(), sessionID, CreateMessageParams{
		Role:  Tool,
		Parts: []ContentPart{ToolResult{ToolCallID: "call_archived", Name: "bash", Content: "old"}},
	})
	require.NoError(t, err)

	summary, err := svc.Create(t.Context(), sessionID, CreateMessageParams{
		Role:             Assistant,
		Parts:            []ContentPart{TextContent{Text: "summary"}},
		IsSummaryMessage: true,
	})
	require.NoError(t, err)
	require.NotEqual(t, archived.ID, summary.ID)

	got, err := svc.ListToolMessages(t.Context(), sessionID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "call_archived", got[0].ToolResults()[0].ToolCallID,
		"an archived result must still be reachable through recall")
}
