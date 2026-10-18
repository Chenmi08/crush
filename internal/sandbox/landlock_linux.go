//go:build linux

package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Landlock access classes. The backend handles only write classes: reads stay
// unconfined because Landlock cannot express "allow everything except", and
// handling a read class would confine reads.
const (
	landlockAccessWriteFile  = unix.LANDLOCK_ACCESS_FS_WRITE_FILE
	landlockAccessRemoveDir  = unix.LANDLOCK_ACCESS_FS_REMOVE_DIR
	landlockAccessRemoveFile = unix.LANDLOCK_ACCESS_FS_REMOVE_FILE
	landlockAccessMakeChar   = unix.LANDLOCK_ACCESS_FS_MAKE_CHAR
	landlockAccessMakeDir    = unix.LANDLOCK_ACCESS_FS_MAKE_DIR
	landlockAccessMakeReg    = unix.LANDLOCK_ACCESS_FS_MAKE_REG
	landlockAccessMakeSock   = unix.LANDLOCK_ACCESS_FS_MAKE_SOCK
	landlockAccessMakeFifo   = unix.LANDLOCK_ACCESS_FS_MAKE_FIFO
	landlockAccessMakeBlock  = unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK
	landlockAccessMakeSym    = unix.LANDLOCK_ACCESS_FS_MAKE_SYM
)

// Landlock ABI versions that introduced individual access classes.
const (
	landlockABIRefer    = 2 // REFER
	landlockABITruncate = 3 // TRUNCATE
	landlockABIIoctlDev = 5 // IOCTL_DEV
)

// Environment variables the helper reads. Only the descriptor number travels
// here: the policy itself travels over the descriptor, never through argv or
// the environment, both of which the confined process can read.
const (
	// HelperEnv asks the current executable to act as the Landlock helper.
	HelperEnv = "CRUSH_LANDLOCK_HELPER"
	// PolicyFDEnv names the inherited descriptor carrying the policy.
	PolicyFDEnv = "CRUSH_LANDLOCK_POLICY_FD"
	// ProbeEnv asks the helper to apply a trivial ruleset and exit, without
	// replacing itself with a target command.
	ProbeEnv = "CRUSH_LANDLOCK_PROBE"
	// HelperSubcommand is the hidden CLI subcommand that runs the helper. It
	// must be the first argv token: the helper is entered through the Cobra
	// command tree, not by inspecting the environment alone.
	HelperSubcommand = "landlock-helper"
)

// landlockBackend confines a child process with the kernel's Landlock LSM.
//
// The restriction is irreversible for the process that applies it, so it is
// applied in a helper process (a hidden subcommand of the Crush binary) that
// then replaces itself with the target command. Crush itself is never
// restricted.
type landlockBackend struct {
	abiOnce sync.Once
	abi     int
	abiErr  error
}

func (b *landlockBackend) Name() string { return "landlock" }

// Enforcement reports partial whenever the policy needs a protected path
// subtracted from a writable root, because Landlock can only union allowlists.
// It also reports partial when the kernel exposes fewer access classes than
// the policy assumes.
func (b *landlockBackend) Enforcement(p *Policy) Enforcement {
	if p == nil {
		return EnforcementPartial
	}
	if p.RequiresSubtraction() {
		return EnforcementPartial
	}
	if abi, err := landlockABI(); err != nil || abi < landlockABITruncate {
		return EnforcementPartial
	}
	if len(p.DenyWrites) > 0 {
		return EnforcementPartial
	}
	return EnforcementFull
}

// AvailabilityError is the fail-closed message for a host without Landlock.
func landlockUnavailable(err error) error {
	return fmt.Errorf("landlock is not available on this kernel (%v); install bubblewrap (bwrap), or run with sandbox mode danger-full-access to run unconfined", err)
}

// landlockABI queries the kernel's Landlock ABI version.
func landlockABI() (int, error) {
	version, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		0, 0,
		unix.LANDLOCK_CREATE_RULESET_VERSION,
	)
	if errno != 0 {
		return 0, errno
	}
	if int(version) <= 0 {
		return 0, errors.New("landlock reports no ABI version")
	}
	return int(version), nil
}

func (b *landlockBackend) cachedABI() (int, error) {
	b.abiOnce.Do(func() {
		b.abi, b.abiErr = landlockABI()
	})
	return b.abi, b.abiErr
}

// Available probes functionally: it runs the helper, which applies a real
// ruleset and exits. A version check alone would pass on a kernel with
// Landlock compiled out or disabled at boot, and on a container that blocks
// the syscalls.
func (b *landlockBackend) Available(ctx context.Context) error {
	if _, err := b.cachedABI(); err != nil {
		return landlockUnavailable(err)
	}
	exe, err := ExecutablePath()
	if err != nil {
		return fmt.Errorf("cannot locate the Crush executable for the landlock helper: %w", err)
	}
	cmd := exec.CommandContext(ctx, exe, HelperSubcommand)
	cmd.Env = append(os.Environ(),
		HelperEnv+"=1",
		ProbeEnv+"=1",
		PolicyFDEnv+"=0",
	)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return landlockUnavailable(fmt.Errorf("%v: %s", err, stderr.String()))
	}
	return nil
}

// Wrap rewrites cmd to run through the helper, passing the policy over a pipe.
func (b *landlockBackend) Wrap(ctx context.Context, p *Policy, cmd *exec.Cmd) (func(), error) {
	if _, err := b.cachedABI(); err != nil {
		return nil, &RunnerError{Backend: b.Name(), Platform: runtime.GOOS, Detail: err.Error()}
	}
	exe, err := ExecutablePath()
	if err != nil {
		return nil, &RunnerError{Backend: b.Name(), Platform: runtime.GOOS, Detail: err.Error()}
	}

	policy, err := marshalLandlockPolicy(p)
	if err != nil {
		return nil, err
	}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("landlock: creating the policy pipe: %w", err)
	}
	if _, err := writeEnd.Write(policy); err != nil {
		readEnd.Close()
		writeEnd.Close()
		return nil, fmt.Errorf("landlock: writing the policy: %w", err)
	}
	writeEnd.Close()

	target := cmd.Path
	targetArgs := cmd.Args[1:]

	// The helper is a hidden subcommand, so its argv must name it: without
	// the "landlock-helper" token the Crush binary would treat the target as
	// a command name of its own and exit with a usage error, making the
	// fallback backend unusable.
	cmd.Path = exe
	cmd.Args = append([]string{exe, HelperSubcommand}, target)
	cmd.Args = append(cmd.Args, targetArgs...)
	// ExtraFiles[0] becomes fd 3 in the child; the descriptor number is all
	// the environment carries.
	cmd.ExtraFiles = append(cmd.ExtraFiles, readEnd)
	// The command's environment wins over the parent's: cmd.Env was built by
	// the caller (the shell), and appending os.Environ() after it would let
	// the parent's values override exports set on the command.
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = append(env,
		HelperEnv+"=1",
		PolicyFDEnv+"=3",
	)
	cleanup := func() {
		readEnd.Close()
	}
	return cleanup, nil
}

// landlockPolicy is the wire form passed to the helper. It carries only paths
// and the mode: the helper re-derives the rule set from these.
type landlockPolicy struct {
	Mode       string   `json:"mode"`
	WriteRoots []string `json:"write_roots"`
	DenyWrites []string `json:"deny_writes,omitempty"`
	Protected  []string `json:"protected,omitempty"`
	ABI        int      `json:"abi"`
}

func marshalLandlockPolicy(p *Policy) ([]byte, error) {
	if p == nil {
		return nil, errors.New("landlock: nil policy")
	}
	abi, _ := landlockABI()
	return json.Marshal(landlockPolicy{
		Mode:       string(p.Mode),
		WriteRoots: p.WriteRootsFor(),
		DenyWrites: p.DenyWrites,
		Protected:  p.Protected,
		ABI:        abi,
	})
}

// fileAccess returns the access classes valid for a non-directory path. The
// kernel rejects directory-only classes (MAKE_*, REMOVE_*, REFER) on a file
// rule with EINVAL.
func fileAccess(abi int) uint64 {
	access := uint64(landlockAccessWriteFile)
	if abi >= landlockABITruncate {
		access |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	return access
}

// handledAccess returns the write access classes the kernel should govern for
// the given ABI. Classes a kernel does not know are omitted: passing an unknown
// bit to landlock_create_ruleset fails with EINVAL.
func handledAccess(abi int) uint64 {
	access := uint64(landlockAccessWriteFile |
		landlockAccessRemoveDir | landlockAccessRemoveFile |
		landlockAccessMakeChar | landlockAccessMakeDir | landlockAccessMakeReg |
		landlockAccessMakeSock | landlockAccessMakeFifo | landlockAccessMakeBlock |
		landlockAccessMakeSym)
	if abi >= landlockABIRefer {
		access |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= landlockABITruncate {
		access |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	return access
}

// applyLandlock installs the policy in the current process. It is
// irreversible: the caller is the helper process, which immediately execs the
// target command.
func applyLandlock(p landlockPolicy) error {
	// no-new-privileges is required before a ruleset can be enforced.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("landlock: setting no-new-privileges: %w", err)
	}

	handled := handledAccess(p.ABI)
	// Only handled_access_fs is meaningful; the remaining fields are zero,
	// which the kernel accepts as zero padding for a newer struct.
	attr := unix.LandlockRulesetAttr{Handled_access_fs: handled}
	fd, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)),
		unsafe.Sizeof(attr),
		0,
	)
	if errno != 0 {
		return fmt.Errorf("landlock: creating the ruleset: %w", errno)
	}
	rulesetFD := int(fd)
	defer unix.Close(rulesetFD)

	// Landlock cannot subtract: protected paths and denials inside a writable
	// root are simply still allowed, which the enforcement level reports.
	for _, root := range p.WriteRoots {
		if err := addLandlockPathRule(rulesetFD, root, handled); err != nil {
			return err
		}
	}
	// Required sinks: writes to /dev/null must work in every mode. Landlock
	// rules are allowlists, so a file rule for the sink grants it.
	for _, sink := range []string{"/dev/null"} {
		if _, err := os.Stat(sink); err != nil {
			continue
		}
		if err := addLandlockPathRule(rulesetFD, sink, fileAccess(p.ABI)); err != nil {
			return err
		}
	}

	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(rulesetFD), 0, 0); errno != 0 {
		return fmt.Errorf("landlock: applying the ruleset: %w", errno)
	}
	return nil
}

func addLandlockPathRule(rulesetFD int, path string, allowed uint64) error {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return fmt.Errorf("landlock: opening %s: %w", path, err)
	}
	defer unix.Close(fd)

	attr := unix.LandlockPathBeneathAttr{Allowed_access: allowed, Parent_fd: int32(fd)}
	_, _, errno := unix.Syscall6(
		unix.SYS_LANDLOCK_ADD_RULE,
		uintptr(rulesetFD),
		unix.LANDLOCK_RULE_PATH_BENEATH,
		uintptr(unsafe.Pointer(&attr)),
		0, 0, 0,
	)
	if errno != 0 {
		return fmt.Errorf("landlock: adding a rule for %s: %w", path, errno)
	}
	return nil
}

// ReadLandlockPolicy reads a policy from an inherited descriptor. The
// descriptor is closed after reading so the confined process cannot reread it.
func ReadLandlockPolicy(fd int) (landlockPolicy, error) {
	file := os.NewFile(uintptr(fd), "landlock-policy")
	if file == nil {
		return landlockPolicy{}, errors.New("landlock: invalid policy descriptor")
	}
	defer file.Close()

	var p landlockPolicy
	if err := json.NewDecoder(file).Decode(&p); err != nil {
		return landlockPolicy{}, fmt.Errorf("landlock: decoding the policy: %w", err)
	}
	if p.ABI <= 0 {
		abi, err := landlockABI()
		if err != nil {
			return landlockPolicy{}, err
		}
		p.ABI = abi
	}
	return p, nil
}

// policyFDFromEnv returns the descriptor number the helper should read.
func policyFDFromEnv() (int, error) {
	raw := os.Getenv(PolicyFDEnv)
	if raw == "" {
		return 0, fmt.Errorf("%s is not set", PolicyFDEnv)
	}
	fd, err := strconv.Atoi(raw)
	if err != nil || fd <= 0 {
		return 0, fmt.Errorf("invalid %s %q", PolicyFDEnv, raw)
	}
	return fd, nil
}

// ExecutablePath is a seam so tests can point the helper at the test binary.
var ExecutablePath = os.Executable

// RunLandlockHelper is the helper entry point. It detects the helper role from
// the environment, applies the policy, and replaces itself with the target
// command. handled reports whether the helper role applied; code is the exit
// status to use when it did.
//
// It is called from the process entry point (the hidden `crush landlock-helper`
// subcommand, and from a test binary's TestMain) so the restriction is applied
// in a process that is about to exec the target, never in Crush itself.
func RunLandlockHelper(args []string) (handled bool, code int) {
	if os.Getenv(HelperEnv) == "" {
		return false, 0
	}

	// The subcommand token is part of argv when the helper is reached
	// through the CLI, and absent when a test binary calls this directly.
	// Strip it so the remaining argv is exactly the target command.
	if len(args) > 0 && args[0] == HelperSubcommand {
		args = args[1:]
	}

	if os.Getenv(ProbeEnv) != "" {
		// Apply a minimal ruleset over the caller's temp directory and exit:
		// this proves the kernel accepts the syscalls without confining
		// anything else.
		probe := landlockPolicy{
			Mode:       string(ModeWorkspaceWrite),
			WriteRoots: []string{os.TempDir()},
		}
		abi, err := landlockABI()
		if err != nil {
			fmt.Fprintf(os.Stderr, "landlock probe: %v\n", err)
			return true, 1
		}
		probe.ABI = abi
		if err := applyLandlock(probe); err != nil {
			fmt.Fprintf(os.Stderr, "landlock probe: %v\n", err)
			return true, 1
		}
		return true, 0
	}

	fd, err := policyFDFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "landlock helper: %v\n", err)
		return true, 126
	}
	policy, err := ReadLandlockPolicy(fd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "landlock helper: %v\n", err)
		return true, 126
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "landlock helper: no target command")
		return true, 126
	}
	if err := applyLandlock(policy); err != nil {
		fmt.Fprintf(os.Stderr, "landlock helper: %v\n", err)
		return true, 126
	}

	target, err := exec.LookPath(args[0])
	if err != nil {
		// A path-prefixed target is used as-is.
		target = args[0]
	}
	if err := unix.Exec(target, args, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "landlock helper: exec %s: %v\n", target, err)
		return true, 126
	}
	return true, 0
}
