package agent

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
)

// todoReminderEmpty is shown when a session has no todo list. It is the
// reminder every turn carried before the list was computed, kept verbatim
// because it is still correct in exactly this case.
const todoReminderEmpty = `This is a reminder that your todo list is currently empty. DO NOT mention this to the user explicitly because they are already aware.
If you are working on tasks that would benefit from a todo list please use the "todos" tool to create one.
If not, please feel free to ignore. Again do not mention this message to the user.`

// todoReminderWithList is shown when the session has a todo list of its own.
// It points at the tool rather than restating the protocol, and forbids
// surfacing the reminder for the same reason the empty variant does: the user
// already sees the list in the UI.
const todoReminderWithList = `This is a reminder of the todo list for this session. Keep it current with the "todos" tool as you work: mark a task in_progress before starting it and completed as soon as it is done. DO NOT mention this reminder to the user explicitly because they are already aware; the list itself is already visible to them.`

// goalRecitationLimit is the byte budget for the goal quoted at the tail of
// the prompt. A goal is a request, not a document, so a long paste is cut:
// carrying it twice would pay for it twice, and the verbatim copy is already
// in the history above.
const goalRecitationLimit = 400

// todoReminder builds the head-of-prompt block that tells the model what the
// session's todo list currently is. A session with no list gets the
// historical "your todo list is currently empty" text verbatim; a session
// with one gets a rendering of it, so the model is not told a stale fact on
// every turn.
//
// The list is rendered in stored order because that is the order the model
// wrote it in: reordering would imply a priority the data does not carry.
func todoReminder(todos []session.Todo) string {
	if len(todos) == 0 {
		return todoReminderEmpty
	}

	var sb strings.Builder
	sb.WriteString(todoReminderWithList)
	sb.WriteString("\n")
	for _, todo := range todos {
		sb.WriteString("\n- [")
		sb.WriteString(string(todo.Status))
		sb.WriteString("] ")
		sb.WriteString(todo.Content)
		if todo.ActiveForm != "" {
			sb.WriteString(" (")
			sb.WriteString(todo.ActiveForm)
			sb.WriteString(")")
		}
	}
	return sb.String()
}

// goalRecitation builds the tail block that restates what the turn is trying
// to do. It is deliberately redundant with the head reminder: long-context
// retrieval follows a U-curve, so the extremes are the positions that
// survive, and a reminder only at the head is the one most likely to be
// skimmed past by the time the model is several tool calls deep.
//
// goal is the prompt of the turn being served, which is not yet in msgs: Run
// snapshots the session before it stores the new user message. Reciting the
// newest stored message instead would name the *previous* request on every
// turn, and the summary narrative right after a compaction. msgs is the
// fallback for callers that have no prompt of their own.
//
// The task is the in-progress todo, or the first pending one when nothing is
// in progress, mirroring how the todos tool asks the model to keep exactly
// one item in progress.
//
// Both halves are optional: a session with neither (a bare tool-only
// continuation, for instance) gets no block at all rather than an empty
// recitation that would read as a new instruction.
func goalRecitation(goal string, msgs []message.Message, todos []session.Todo) string {
	if goal == "" {
		goal = latestUserGoal(msgs)
	} else {
		goal = clipToBytes(normalizeGoal(goal), goalRecitationLimit)
	}
	task := currentTodo(todos)
	if goal == "" && task == "" {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("This is a reminder of what you are currently working on. It restates your own task; it is not a new request and must not be repeated to the user.")
	if goal != "" {
		sb.WriteString("\n\nCurrent goal: ")
		sb.WriteString(goal)
	}
	if task != "" {
		sb.WriteString("\n\nCurrent task: ")
		sb.WriteString(task)
	}
	return sb.String()
}

// latestUserGoal returns the most recent non-empty user message, clipped for
// the recitation. A hidden continuation still counts: it is the instruction
// the model is acting on, even though the UI does not show it.
func latestUserGoal(msgs []message.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != message.User {
			continue
		}
		if text := normalizeGoal(msgs[i].Content().Text); text != "" {
			return clipToBytes(text, goalRecitationLimit)
		}
	}
	return ""
}

// currentTodo names the task the model should be on: the in-progress item if
// there is one, otherwise the first pending item. A completed-only list names
// nothing, because every remaining task is done.
func currentTodo(todos []session.Todo) string {
	for _, todo := range todos {
		if todo.Status == session.TodoStatusInProgress {
			return todoLabel(todo)
		}
	}
	for _, todo := range todos {
		if todo.Status == session.TodoStatusPending {
			return todoLabel(todo)
		}
	}
	return ""
}

// todoLabel prefers the present-continuous form for the recitation: the tail
// block says what the model is doing, not what it was asked to do.
func todoLabel(todo session.Todo) string {
	if todo.ActiveForm != "" {
		return todo.ActiveForm
	}
	return todo.Content
}

// normalizeGoal flattens a user message onto one line. A multi-line paste in
// the recitation would push the block's own framing far away from the text it
// frames, and the verbatim copy is already in the history.
func normalizeGoal(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// clipToBytes cuts s to at most max bytes without splitting a rune. The
// ellipsis is only appended when something was actually dropped, and the
// budget accounts for it: the result never exceeds max.
func clipToBytes(s string, max int) string {
	const ellipsis = "…"
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	if max <= len(ellipsis) {
		return ellipsis
	}
	// Reserve the marker's bytes so the clipped value honors the budget
	// instead of exceeding it by three.
	cut := max - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimRight(s[:cut], " \t") + ellipsis
}
