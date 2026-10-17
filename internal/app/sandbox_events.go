package app

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/sandbox"
)

// SandboxDenialMsg is a transient, non-blocking notice that a write was
// refused by the sandbox. It surfaces in the status line for a few seconds so
// frequent refusals are visible without reading the conversation, and without
// interrupting the turn: authorization stays model-initiated by design.
type SandboxDenialMsg struct {
	sandbox.DenialNotice
}

// subscribeSandboxDenials forwards sandbox refusal notices onto the app event
// stream as SandboxDenialMsg values. Refusals that happen before this runs
// (or in non-TUI commands) are counted and logged but not displayed.
//
// The registration is process-global while the app is not, so shutdown
// unregisters: a second app instance in one process (tests) must not receive
// the first one's refusals.
func subscribeSandboxDenials(ctx context.Context, events *pubsub.Broker[tea.Msg]) {
	sandbox.NotifyDenials(func(notice sandbox.DenialNotice) {
		events.Publish(pubsub.UpdatedEvent, SandboxDenialMsg{DenialNotice: notice})
	})
	go func() {
		<-ctx.Done()
		sandbox.NotifyDenials(nil)
	}()
}
