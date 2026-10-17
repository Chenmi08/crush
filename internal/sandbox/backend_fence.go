package sandbox

import (
	"context"
	"os/exec"
)

// fenceBackend is the in-process backend: it establishes no OS boundary and
// therefore reports partial enforcement. The shell's file-open hook and the
// mutating file tools still enforce the same policy, so every write Crush
// itself performs is fenced, but child processes are not.
//
// It exists so a user can observe a real boundary end to end before the OS
// backends land; a platform with a real backend never selects it.
type fenceBackend struct{}

func (fenceBackend) Name() string { return "in-process" }

// Available always succeeds: the fence needs no external dependency.
func (fenceBackend) Available(context.Context) error { return nil }

func (fenceBackend) Enforcement(*Policy) Enforcement { return EnforcementPartial }

// Wrap does nothing: the in-process fence is applied by the shell and the file
// tools, not by wrapping a process.
func (fenceBackend) Wrap(context.Context, *Policy, *exec.Cmd) (func(), error) { return nil, nil }
