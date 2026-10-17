package server

import (
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/sandbox"
)

// Sandbox members shared by the coordinator fakes in this package. They are
// declared on each fake via embedding of this struct so a fake only carries
// what its test exercises.
type sandboxFake struct{}

func (sandboxFake) SetSandboxMode(sandbox.Mode, bool) error { return nil }

func (sandboxFake) SandboxMode() sandbox.Mode { return sandbox.DefaultMode }

func (sandboxFake) SandboxStatus() config.SandboxRuntime { return config.SandboxRuntime{} }
