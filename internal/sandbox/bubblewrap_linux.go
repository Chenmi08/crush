//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// bubblewrapBinary is the runner this backend invokes.
const bubblewrapBinary = "bwrap"

// bubblewrapProbeTimeout bounds the functional probe. A runner that hangs is
// as unusable as one that is missing, and startup must not stall on it.
const bubblewrapProbeTimeout = 2 * time.Second

// bubblewrapBackend confines a child process with bubblewrap: it remounts the
// whole filesystem read-only, then remounts each writable root, then remounts
// protected paths read-only again (last mount wins). That subtraction is why
// it is preferred over Landlock.
type bubblewrapBackend struct {
	pathOnce sync.Once
	path     string
	pathErr  error
}

func (b *bubblewrapBackend) Name() string { return "bubblewrap" }

// Enforcement is full: bubblewrap can subtract protected paths from a
// writable root.
func (b *bubblewrapBackend) Enforcement(*Policy) Enforcement { return EnforcementFull }

// Available probes the runner functionally: it applies a minimal profile
// around `true`. A version check would pass on hosts where namespaces are
// forbidden (containers, some distro defaults), so it runs the real thing.
func (b *bubblewrapBackend) Available(ctx context.Context) error {
	_, err := b.resolve()
	if err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, bubblewrapProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, b.path,
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--unshare-pid",
		"--die-with-parent",
		"--",
		"/bin/true",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s cannot create its namespaces: %v: %s", bubblewrapBinary, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// resolve locates the runner once and caches the result.
func (b *bubblewrapBackend) resolve() (string, error) {
	b.pathOnce.Do(func() {
		path, ok := availableOnPath(bubblewrapBinary)
		if !ok {
			b.pathErr = fmt.Errorf("%s is not installed (install bubblewrap, or run with sandbox mode danger-full-access to run unconfined)", bubblewrapBinary)
			return
		}
		b.path = path
	})
	return b.path, b.pathErr
}

// Wrap rewrites cmd so it executes inside a bubblewrap sandbox.
func (b *bubblewrapBackend) Wrap(ctx context.Context, p *Policy, cmd *exec.Cmd) (func(), error) {
	path, err := b.resolve()
	if err != nil {
		return nil, &RunnerError{Backend: b.Name(), Platform: runtime.GOOS, Detail: err.Error()}
	}
	args, artifacts, err := bubblewrapArgs(p, cmd.Path, cmd.Args[1:])
	if err != nil {
		return nil, err
	}
	cmd.Path = path
	cmd.Args = append([]string{path}, args...)
	return cleanupArtifacts(p, artifacts), nil
}

// BubblewrapArgs generates the runner arguments for a policy and a target
// command. It is exported so the generated rules can be characterized by
// golden tests, which is what keeps one backend from silently granting a root
// another does not.
func BubblewrapArgs(p *Policy, target string, targetArgs []string) ([]string, error) {
	args, _, err := bubblewrapArgs(p, target, targetArgs)
	return args, err
}

// bubblewrapArgs is BubblewrapArgs plus the list of paths bubblewrap will
// create as mount points, which the caller removes after the command exits.
func bubblewrapArgs(p *Policy, target string, targetArgs []string) ([]string, []string, error) {
	if p == nil {
		return nil, nil, errors.New("bubblewrap: nil policy")
	}
	var artifacts []string

	args := []string{
		// Everything is read-only unless remounted below. Reads are
		// deliberately unconfined.
		"--ro-bind", "/", "/",
		// Pseudo-filesystems the runner needs to function.
		"--dev", "/dev",
		"--proc", "/proc",
	}

	// Writable roots, in policy order, so generated args are deterministic.
	for _, root := range p.WriteRoots {
		if _, err := os.Stat(root); err != nil {
			// Only bind roots that exist. A missing root is reported at
			// resolution time for user-granted roots; the private temp
			// directory is created by Resolve.
			continue
		}
		args = append(args, "--bind", root, root)
	}

	// Protected paths are remounted read-only after the writable roots, so
	// the last mount wins. A path that does not exist yet is mounted from
	// /dev/null so it cannot be created inside the sandbox.
	//
	// A protected path covered by a protected ancestor is skipped: mounting
	// a file over the parent would make the child's parent a file, and the
	// ancestor's mount already denies the whole subtree.
	for _, prot := range p.Protected {
		if !protectedInsideAnyRoot(p, prot) {
			continue
		}
		if hasProtectedAncestor(p, prot) {
			continue
		}
		if _, err := os.Stat(prot); err == nil {
			args = append(args, "--ro-bind", prot, prot)
			continue
		}
		if !parentExists(prot) {
			// The parent does not exist either, so the path cannot be
			// created without first creating the parent, which the
			// writable-root mount alone would allow. The parent-protected
			// case is handled by hasProtectedAncestor above; for an
			// unrelated missing parent the ancestor's own protection (or
			// the fact that it is outside the workspace) covers it.
			continue
		}
		// The path does not exist yet. Bind a placeholder file over it so
		// the confined process cannot create it. Binding /dev/null itself
		// would not work: /dev/null is a symlink, and bwrap would mount
		// the symlink's target rather than deny the path.
		placeholder, err := p.protectedPlaceholder()
		if err != nil {
			return nil, nil, err
		}
		args = append(args, "--ro-bind", placeholder, prot)
		artifacts = append(artifacts, prot)
	}

	// Mount denials from a tmpfs so they are writable-nothing, cheap, and
	// also work when the path does not exist yet.
	for _, deny := range p.DenyWrites {
		if info, err := os.Stat(deny); err == nil && info.IsDir() {
			args = append(args, "--tmpfs", deny)
			continue
		}
		if parentExists(deny) {
			args = append(args, "--tmpfs", filepath.Dir(deny))
		}
	}

	args = append(args,
		"--unshare-pid",
		"--die-with-parent",
		"--new-session",
		"--chdir", p.Workspace,
	)

	// The private temp directory replaces /tmp semantics for tools that
	// honour TMPDIR, without exposing a shared /tmp.
	if p.TempDir != "" {
		args = append(args, "--setenv", "TMPDIR", p.TempDir)
	}

	args = append(args, "--", target)
	return append(args, targetArgs...), artifacts, nil
}

// hasProtectedAncestor reports whether an ancestor of prot is itself
// protected, in which case the ancestor's mount already covers it.
func hasProtectedAncestor(p *Policy, prot string) bool {
	for _, other := range p.Protected {
		if other == prot {
			continue
		}
		if within(prot, other) && prot != other {
			return true
		}
	}
	return false
}

func parentExists(path string) bool {
	_, err := os.Stat(filepath.Dir(path))
	return err == nil
}

// protectedInsideAnyRoot reports whether prot needs a read-only remount: it
// only matters when it sits inside a writable root.
func protectedInsideAnyRoot(p *Policy, prot string) bool {
	for _, root := range p.WriteRoots {
		if within(prot, root) {
			return true
		}
	}
	return false
}
