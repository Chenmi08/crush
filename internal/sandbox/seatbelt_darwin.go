//go:build darwin

package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// seatbeltBinary is the runner this backend invokes. The PRD requires the
// absolute path, because a PATH lookup would let a project-local binary
// impersonate the sandbox.
const seatbeltBinary = "/usr/bin/sandbox-exec"

// seatbeltProbeTimeout bounds the functional probe.
const seatbeltProbeTimeout = 2 * time.Second

// seatbeltBackend confines a child process with macOS Seatbelt by generating a
// profile and running the command through sandbox-exec.
type seatbeltBackend struct {
	pathOnce sync.Once
	path     string
	pathErr  error
}

func (b *seatbeltBackend) Name() string { return "seatbelt" }

// Enforcement is full: Seatbelt subtracts via a trailing deny form, because
// the last match wins.
func (b *seatbeltBackend) Enforcement(*Policy) Enforcement { return EnforcementFull }

// resolve checks the runner exists at its absolute path.
func (b *seatbeltBackend) resolve() (string, error) {
	b.pathOnce.Do(func() {
		info, err := os.Stat(seatbeltBinary)
		if err != nil {
			b.pathErr = fmt.Errorf("%s is not available (macOS sandbox-exec is required; run with sandbox mode danger-full-access to run unconfined)", seatbeltBinary)
			return
		}
		if info.IsDir() {
			b.pathErr = fmt.Errorf("%s is a directory", seatbeltBinary)
			return
		}
		b.path = seatbeltBinary
	})
	return b.path, b.pathErr
}

// Available probes the runner functionally by applying a real profile around
// /usr/bin/true.
func (b *seatbeltBackend) Available(ctx context.Context) error {
	path, err := b.resolve()
	if err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, seatbeltProbeTimeout)
	defer cancel()
	profile, err := SeatbeltProfile(&Policy{
		Mode:       ModeReadOnly,
		Workspace:  "/",
		WriteRoots: nil,
		TempDir:    "/tmp",
	})
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(probeCtx, path, "-p", profile, "/usr/bin/true")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s cannot apply its profile: %v: %s", seatbeltBinary, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Wrap rewrites cmd so it executes inside a Seatbelt sandbox.
func (b *seatbeltBackend) Wrap(ctx context.Context, p *Policy, cmd *exec.Cmd) (func(), error) {
	path, err := b.resolve()
	if err != nil {
		return nil, &RunnerError{Backend: b.Name(), Platform: runtime.GOOS, Detail: err.Error()}
	}
	profile, err := SeatbeltProfile(p)
	if err != nil {
		return nil, err
	}
	artifacts, err := seatbeltMountPoints(p)
	if err != nil {
		return nil, err
	}
	target := cmd.Path
	cmd.Path = path
	cmd.Args = append([]string{path, "-p", profile, target}, cmd.Args[1:]...)
	return cleanupArtifacts(p, artifacts), nil
}

// seatbeltMountPoints returns the protected and denied paths that do not exist
// yet. Seatbelt expresses them in the profile text alone, so nothing is
// created; the list is returned for symmetry with the other backends and to
// keep the cleanup contract uniform.
func seatbeltMountPoints(p *Policy) ([]string, error) {
	var missing []string
	for _, path := range append(append([]string{}, p.Protected...), p.DenyWrites...) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			missing = append(missing, path)
		}
	}
	return missing, nil
}
