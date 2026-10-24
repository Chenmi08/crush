package tools

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
)

//go:embed recall.md
var recallDescription string

// RecallToolName is the name the model calls to read an archived tool result.
const RecallToolName = "recall"

const (
	// recallDefaultLimit is how much of a result is returned when the model
	// does not ask for a specific range. It is large enough for a typical
	// error report and small enough that a page does not itself become the
	// problem the tool exists to solve.
	recallDefaultLimit = 8192

	// recallMaxLimit bounds a single call. Reading more than a page at once
	// is almost never what the caller needs, and allowing it would let one
	// `recall` refill the window that compaction just emptied.
	recallMaxLimit = 65536
)

// RecallParams are the arguments to the `recall` tool. ToolCallID selects
// what to read; Offset and Limit slice the body by bytes.
type RecallParams struct {
	ToolCallID string `json:"tool_call_id,omitempty" description:"ID of the tool call whose archived result to retrieve"`
	Offset     int    `json:"offset,omitempty" description:"Byte offset into the archived result to start from (default 0)"`
	Limit      int    `json:"limit,omitempty" description:"Maximum number of bytes to return (default 8192, max 65536)"`
}

// NewRecallTool returns the `recall` tool. It reads only the caller's own
// session, so a sub-agent cannot pull in its parent's history.
func NewRecallTool(messages message.Service) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		RecallToolName,
		recallDescription,
		func(ctx context.Context, params RecallParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.NewTextErrorResponse("recall requires a session and none is attached to this turn"), nil
			}
			if params.ToolCallID == "" {
				return fantasy.NewTextErrorResponse(
					"recall needs `tool_call_id`: the id of the tool call whose archived result you want",
				), nil
			}

			result, recordedAt, err := findArchivedToolResult(ctx, messages, sessionID, params.ToolCallID)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			body, start, end, more := recallSlice(result.Content, params.Offset, params.Limit)
			return fantasy.NewTextResponse(recallResponse(result, recordedAt, body, start, end, more)), nil
		},
	)
}

// findArchivedToolResult scans the session's tool messages for a result with
// the given call id. It returns the result and the time the carrying message
// was recorded, which is what the response reports as `recorded_at`.
//
// An unknown id is an error rather than an empty result: silently returning
// nothing would read as "the result was empty", which is a different and
// misleading claim.
func findArchivedToolResult(ctx context.Context, messages message.Service, sessionID, toolCallID string) (message.ToolResult, int64, error) {
	msgs, err := messages.ListToolMessages(ctx, sessionID)
	if err != nil {
		return message.ToolResult{}, 0, fmt.Errorf("failed to list tool messages: %w", err)
	}
	for _, msg := range msgs {
		for _, result := range msg.ToolResults() {
			if result.ToolCallID == toolCallID {
				return result, msg.CreatedAt, nil
			}
		}
	}
	return message.ToolResult{}, 0, fmt.Errorf(
		"no archived tool result with tool_call_id %q in this session; ids from other sessions, parent sessions or sub-agent sessions are not visible here",
		toolCallID,
	)
}

// recallResponse renders the header, a page of the archived body, and the
// notice that tells the caller whether the record is complete.
func recallResponse(result message.ToolResult, recordedAt int64, body string, start, end int, more bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[recall] tool_call_id=%s", result.ToolCallID)
	if result.Name != "" {
		fmt.Fprintf(&sb, " name=%s", result.Name)
	}
	// The exit code is the first thing a caller checks when deciding
	// whether an archived command result is worth reading, so surface it
	// in the header rather than making them fetch the body to find it.
	// Only bash results carry the status line; a file view whose tail
	// quotes "Exit code 3" must not be reported as a failure.
	if IsBashToolName(result.Name) {
		if code, ok := ParseExitCode(result.Content); ok {
			fmt.Fprintf(&sb, " exit=%d", code)
		}
	}
	fmt.Fprintf(&sb, " bytes=%d\n", len(result.Content))
	fmt.Fprintf(&sb, "recorded_at=%s (archived record; may not reflect current state)\n\n",
		time.Unix(recordedAt, 0).UTC().Format(time.RFC3339))
	sb.WriteString(body)

	total := len(result.Content)
	switch {
	case total == 0:
		sb.WriteString("\n\n(archived result is empty)")
	case start >= total:
		fmt.Fprintf(&sb, "\n\n(requested offset is at or past the end of the archived result, which is %d bytes)", total)
	case more:
		fmt.Fprintf(&sb, "\n\n(returned bytes %d-%d of %d; call again with offset=%d for more)", start, end, total, end)
	}
	return sb.String()
}

// recallSlice returns the requested byte range of s without splitting a
// multi-byte rune at either end, plus the effective start and end offsets and
// whether more bytes follow.
//
// The start moves forward to a rune boundary and the end moves back to one,
// so concatenating successive pages can drop a rune but can never produce
// invalid UTF-8.
func recallSlice(s string, offset, limit int) (body string, start, end int, more bool) {
	offset, limit = recallWindow(offset, limit)
	start = runeStartAtOrAfter(s, offset)
	if start >= len(s) {
		return "", start, start, false
	}
	end = runeStartAtOrBefore(s, start+limit)
	return s[start:end], start, end, end < len(s)
}

// recallWindow normalises the requested byte range. A negative offset or
// limit is treated as absent rather than rejected, because the zero value of
// a missing field and a deliberately negative one are indistinguishable to
// caller and only one of them is a mistake worth spending a turn on.
func recallWindow(offset, limit int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = recallDefaultLimit
	}
	if limit > recallMaxLimit {
		limit = recallMaxLimit
	}
	return offset, limit
}
