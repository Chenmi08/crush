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

// RecallParams are the arguments to the `recall` tool. Exactly one of
// ToolCallID, MessageID and SpillPath selects what to read; Offset and Limit
// slice the body by bytes.
type RecallParams struct {
	ToolCallID string `json:"tool_call_id,omitempty" description:"ID of the tool call whose archived result to retrieve"`
	MessageID  string `json:"message_id,omitempty" description:"ID of an archived user message, as listed under anchors: in the context manifest"`
	SpillPath  string `json:"spill_path,omitempty" description:"Path of a file Crush spilled because its content was truncated; only files under the spill directory are accepted"`
	Offset     int    `json:"offset,omitempty" description:"Byte offset into the archived content to start from (default 0)"`
	Limit      int    `json:"limit,omitempty" description:"Maximum number of bytes to return (default 8192, max 65536)"`
}

// NewRecallTool returns the `recall` tool. Archived results are read only
// from the caller's own session, so a sub-agent cannot pull in its parent's
// history; spill files are read only from the directories Crush writes them
// to, so this never becomes a second `view`.
//
// spillDir is the data directory spill files live under. A sandboxed call may
// have written into the policy's private temp directory instead, which the
// tool also accepts; both are Crush-owned locations.
func NewRecallTool(messages message.Service, spillDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		RecallToolName,
		recallDescription,
		func(ctx context.Context, params RecallParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			selectors := 0
			for _, selector := range []string{params.ToolCallID, params.MessageID, params.SpillPath} {
				if selector != "" {
					selectors++
				}
			}
			switch {
			case selectors > 1:
				return fantasy.NewTextErrorResponse(
					"recall takes exactly one of `tool_call_id` (an archived tool result), `message_id` (an archived user message) or `spill_path` (a file named in a truncation notice), not several",
				), nil
			case selectors == 0:
				return fantasy.NewTextErrorResponse(
					"recall needs one of `tool_call_id`, the id of the tool call whose archived result you want; `message_id`, the id of an archived user message; or `spill_path`, the file named in a truncation notice",
				), nil
			}

			if params.SpillPath != "" {
				return recallSpill(ctx, params, spillDir), nil
			}

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.NewTextErrorResponse("recall requires a session and none is attached to this turn"), nil
			}

			if params.MessageID != "" {
				return recallMessage(ctx, messages, sessionID, params), nil
			}

			result, recordedAt, err := findArchivedToolResult(ctx, messages, sessionID, params.ToolCallID)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			page := recallSlice(result.Content, params.Offset, params.Limit)
			return fantasy.NewTextResponse(recallResponse(result, recordedAt, page)), nil
		},
	)
}

// recallSpill reads a page of a spill file Crush wrote when it truncated a
// tool result.
//
// The path is validated against the spill roots rather than trusted: the
// model supplies it, and the tool must not become a general file reader that
// bypasses the view tool's size limit and the sandbox's read rules.
func recallSpill(ctx context.Context, params RecallParams, configured string) fantasy.ToolResponse {
	file, err := resolveSpillPath(params.SpillPath, spillRoots(ctx, configured))
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error())
	}
	defer file.Close() //nolint:errcheck

	page, err := file.readSpillSlice(params.Offset, params.Limit)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error())
	}
	return fantasy.NewTextResponse(spillResponse(file.name, file.modTime, page))
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

// recallMessage reads a page of an archived user message. Compaction keeps
// earlier user requests only as prose in the summary, which paraphrases them;
// the manifest lists their ids under `anchors:` so the exact wording stays
// retrievable, and this is what makes those handles usable.
//
// An archived request is refused if it is not usable as a quote, and the
// refusal says which of the three reasons applies: a summary row, another
// session, or a row that carries tool calls instead of text. Answering
// "0 bytes" to any of them would read as "that record is empty".
func recallMessage(ctx context.Context, messages message.Service, sessionID string, params RecallParams) fantasy.ToolResponse {
	msg, err := messages.Get(ctx, params.MessageID)
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf(
			"could not read archived message %q: %v", params.MessageID, err,
		))
	}
	if msg.SessionID != sessionID {
		return fantasy.NewTextErrorResponse(fmt.Sprintf(
			"no archived message with message_id %q in this session; ids from other sessions, parent sessions or sub-agent sessions are not visible here",
			params.MessageID,
		))
	}
	if msg.IsSummaryMessage {
		return fantasy.NewTextErrorResponse(fmt.Sprintf(
			"message_id %q is a previous summary, not a user message; read the current summary instead",
			params.MessageID,
		))
	}
	if msg.Role != message.User {
		return fantasy.NewTextErrorResponse(nonUserRedirect(msg, params.MessageID))
	}

	page := recallSlice(msg.Content().Text, params.Offset, params.Limit)
	return fantasy.NewTextResponse(messageResponse(msg, page))
}

// nonUserRedirect explains that a message id names a request or a record, not
// a payload, and points at what it does hold. Summaries written before PR-20
// listed a failure anchor as the assistant message that requested the failing
// call, so that id arrives here as `message_id`; naming its tool calls turns
// a dead handle into a working one instead of an empty page.
func nonUserRedirect(msg message.Message, messageID string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb,
		"message_id %q is an archived %s message, not a user message; there is no text to page through",
		messageID, msg.Role,
	)
	if results := msg.ToolResults(); len(results) > 0 {
		// A tool row's payload is its results, which are already
		// addressable by their own handles.
		sb.WriteString(". Read one of these with `tool_call_id`:")
		for _, result := range results {
			fmt.Fprintf(&sb, " %s (%s)", result.ToolCallID, result.Name)
		}
		return sb.String()
	}
	if calls := msg.ToolCalls(); len(calls) > 0 {
		sb.WriteString(". Read the result of one of these with `tool_call_id`:")
		for _, call := range calls {
			fmt.Fprintf(&sb, " %s (%s)", call.ID, call.Name)
		}
		return sb.String()
	}
	sb.WriteString("; it has no tool calls or results, so there is nothing to recall from it")
	return sb.String()
}

// recallPage is one slice of a larger record: the returned bytes, the range
// they cover, and whether anything follows. The three travel together through
// both retrieval forms, so they are one value rather than three arguments
// whose order has to be remembered.
type recallPage struct {
	Body  string
	Start int
	End   int
	Total int
	More  bool
}

// recallResponse renders the header, a page of the archived body, and the
// notice that tells the caller whether the record is complete.
func recallResponse(result message.ToolResult, recordedAt int64, page recallPage) string {
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
	sb.WriteString(page.Body)
	sb.WriteString(page.remainder())
	return sb.String()
}

// messageResponse renders an archived user message. It says what a resuming
// agent needs to judge the quote: whose word it is, how long it was, and when
// it was said, so an old instruction is not mistaken for the current one.
func messageResponse(msg message.Message, page recallPage) string {
	return recallDocument(
		fmt.Sprintf("message_id=%s role=user bytes=%d", msg.ID, page.Total),
		time.Unix(msg.CreatedAt, 0),
		page,
	)
}

// spillResponse renders a page of a spill file. The header carries the same
// fields as an archived result where they exist: the byte count prices the
// retrieval, and the record is just as historical as a tool result.
func spillResponse(path string, recordedAt time.Time, page recallPage) string {
	return recallDocument(fmt.Sprintf("spill_path=%s bytes=%d", path, page.Total), recordedAt, page)
}

// recallDocument assembles the three headers that differ only in their
// leading fields. Keeping the "historical record" notice and the
// body/remainder assembly in one place stops the forms from drifting apart:
// each is the same promise about the same kind of data.
func recallDocument(header string, recordedAt time.Time, page recallPage) string {
	recorded := "unknown"
	if !recordedAt.IsZero() {
		recorded = recordedAt.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf(
		"[recall] %s\nrecorded_at=%s (archived record; may not reflect current state)\n\n%s%s",
		header, recorded, page.Body, page.remainder(),
	)
}

// remainder renders the trailing notice shared by both retrieval forms:
// empty, past the end, or paged.
func (p recallPage) remainder() string {
	switch {
	case p.Total == 0:
		return "\n\n(archived result is empty)"
	case p.Start >= p.Total:
		return fmt.Sprintf("\n\n(requested offset is at or past the end of the archived result, which is %d bytes)", p.Total)
	case p.More:
		return fmt.Sprintf("\n\n(returned bytes %d-%d of %d; call again with offset=%d for more)", p.Start, p.End, p.Total, p.End)
	}
	return ""
}

// recallSlice returns the requested byte range of s without splitting a
// multi-byte rune at either end.
//
// The start moves forward to a rune boundary and the end moves back to one,
// so concatenating successive pages can drop a rune but can never produce
// invalid UTF-8.
func recallSlice(s string, offset, limit int) recallPage {
	offset, limit = recallWindow(offset, limit)
	start := runeStartAtOrAfter(s, offset)
	if start >= len(s) {
		return recallPage{Start: start, Total: len(s)}
	}
	end := runeStartAtOrBefore(s, start+limit)
	return recallPage{
		Body:  s[start:end],
		Start: start,
		End:   end,
		Total: len(s),
		More:  end < len(s),
	}
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
