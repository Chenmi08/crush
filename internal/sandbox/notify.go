package sandbox

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// DenialNotice is the user-facing summary of one policy refusal. It is
// deliberately short: it lands in a transient status line, not a dialog, and
// never blocks the turn. The full diagnostic stays in the tool result the
// model sees.
type DenialNotice struct {
	// Tool is the tool that was refused, when known.
	Tool string
	// Path is the refused path.
	Path string
	// Mode is the mode that applied.
	Mode string
	// Protected reports a refusal that cannot be re-allowed by any setting.
	Protected bool
	// Count is this refusal's number within the process, stamped onto the
	// denial at construction. A growing count tells the user the boundary is
	// being hit repeatedly.
	Count int64
}

func (n DenialNotice) String() string {
	what := "write outside writable roots"
	if n.Protected {
		what = "protected path"
	}
	out := "sandbox denied " + what + ": " + n.Path
	// A repeat tells the user the boundary is being hit repeatedly; one
	// refusal says nothing more.
	if n.Count > 1 {
		out += fmt.Sprintf(" (%d refusals this session)", n.Count)
	}
	return out
}

// DenialCount counts every policy refusal in this process. It is surfaced in
// denial diagnostics so a model that keeps hitting the same boundary notices
// it is going in circles.
var denialCount atomic.Int64

// DenialCount returns the number of policy refusals so far.
func DenialCount() int64 { return denialCount.Load() }

// denialBroker fans refusal notices out to whoever wants to show them (the
// TUI status line). Nil until a subscriber registers; refusals that happen
// with no subscriber are still counted, just not displayed.
var (
	denialMu       sync.RWMutex
	denialNotifier func(DenialNotice)
)

// NotifyDenials registers a callback that receives every subsequent policy
// refusal. The callback must not block: it runs on the tool-execution path.
// Passing nil unregisters.
func NotifyDenials(notify func(DenialNotice)) {
	denialMu.Lock()
	defer denialMu.Unlock()
	denialNotifier = notify
}

// PublishDenial counts a refusal and, when a subscriber is registered, hands
// it a notice. It never blocks and never panics on a nil map or closed
// subscriber.
func PublishDenial(tool, path string, mode string, protected bool) {
	denialMu.RLock()
	notify := denialNotifier
	denialMu.RUnlock()
	if notify == nil {
		return
	}
	notice := DenialNotice{
		Tool:      tool,
		Path:      path,
		Mode:      mode,
		Protected: protected,
		Count:     denialCount.Load(),
	}
	// A subscriber that misbehaves must not break tool execution.
	defer func() { _ = recover() }()
	notify(notice)
}
