package proto

import (
	"errors"

	"github.com/charmbracelet/crush/internal/skills"
)

// SkillDiscoveryState mirrors skills.DiscoveryState across the wire.
// Values must stay in sync with internal/skills.DiscoveryState; do not
// reorder without a coordinated server/client bump.
type SkillDiscoveryState int

const (
	// SkillStateNormal indicates the skill was parsed and validated
	// successfully.
	SkillStateNormal SkillDiscoveryState = iota
	// SkillStateError indicates discovery encountered a scan/parse/validate
	// error.
	SkillStateError
)

// SkillState is the wire representation of skills.SkillState.
type SkillState struct {
	Name                   string              `json:"name"`
	Path                   string              `json:"path"`
	State                  SkillDiscoveryState `json:"state"`
	Error                  string              `json:"error,omitempty"`
	UserInvocable          bool                `json:"user_invocable"`
	DisableModelInvocation bool                `json:"disable_model_invocation"`
}

// SkillStateFromSkills converts a skills.SkillState into its wire form.
// Errors are flattened to strings because error does not round-trip over
// JSON.
func SkillStateFromSkills(s *skills.SkillState) SkillState {
	state := SkillState{
		Name:                   s.Name,
		Path:                   s.Path,
		State:                  SkillDiscoveryState(s.State),
		UserInvocable:          s.UserInvocable,
		DisableModelInvocation: s.DisableModelInvocation,
	}
	if s.Err != nil {
		state.Error = s.Err.Error()
	}
	return state
}

// ToSkillState reconstructs a skills.SkillState from its wire form.
// Non-empty Error strings become synthetic error values; the TUI never
// type-asserts on Err.
func (s SkillState) ToSkillState() *skills.SkillState {
	state := &skills.SkillState{
		Name:                   s.Name,
		Path:                   s.Path,
		State:                  skills.DiscoveryState(s.State),
		UserInvocable:          s.UserInvocable,
		DisableModelInvocation: s.DisableModelInvocation,
	}
	if s.Error != "" {
		state.Err = errors.New(s.Error)
	}
	return state
}

// SkillsEvent is the wire representation of skills.Event.
type SkillsEvent struct {
	States []SkillState `json:"states"`
}
