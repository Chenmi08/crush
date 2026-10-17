package sandbox

import (
	"fmt"
	"strings"
)

// ExitClass is what a backend's non-zero exit means for the boundary. It is
// decided by the backend that ran the command, because each backend fails
// differently: confusing the two would make the model request escalation
// around a broken sandbox, or treat a real refusal as infrastructure failure.
type ExitClass string

const (
	// ExitClassDenial means confinement worked: the kernel or the runner
	// refused an operation. The command ran inside the boundary and was
	// stopped by it.
	ExitClassDenial ExitClass = "denial"
	// ExitClassRunnerFailure means the boundary itself failed: the command
	// never ran (or was killed by the runner before doing anything). The
	// model must not escalate around this.
	ExitClassRunnerFailure ExitClass = "runner-failure"
	// ExitClassOrdinary means the exit is the command's own failure, with no
	// sandbox signature. The overwhelming majority of non-zero exits.
	ExitClassOrdinary ExitClass = "ordinary"
)

// ExitDiagnosis is the classified result of a confined command's non-zero
// exit.
type ExitDiagnosis struct {
	Class ExitClass
	// Backend is the name of the classifier that produced this diagnosis.
	Backend string
	// Detail is the matched signature line, for logs and for the model.
	Detail string
	// ExitCode is the command's exit status.
	ExitCode int
}

func (d ExitDiagnosis) String() string {
	return fmt.Sprintf("sandbox-exit class=%s backend=%s exit=%d detail=%s", d.Class, d.Backend, d.ExitCode, d.Detail)
}

// signature is one backend-specific fatal signature: a substring of the
// command's stderr that identifies the exit's meaning, and the class it
// implies. Signatures are per backend, not a union: a union would claim
// diagnostics a backend never emits.
type signature struct {
	match string
	class ExitClass
}

// runnerFailureSignatures are the ways each backend's boundary fails to
// establish itself after the process has started. A reserved exit code where
// the backend defines one is matched alongside the text.
//
// Sources:
//   - bubblewrap prints "bwrap: <reason>" and exits 1 for setup failures
//     (Can't chdir, Can't find source path, Can't create namespace, ...).
//   - sandbox-exec prints "sandbox-exec: <reason>" and exits 1 for profile
//     failures, and the kernel reports "Operation not permitted" for a
//     profile the kernel refused.
//   - the Landlock helper prints "landlock helper: <reason>" and exits 126
//     before exec'ing the target.
var runnerFailureSignatures = map[string][]signature{
	"bubblewrap": {
		{match: "bwrap: Can't ", class: ExitClassRunnerFailure},
		{match: "bwrap: Failed to ", class: ExitClassRunnerFailure},
		{match: "bwrap: New ", class: ExitClassRunnerFailure},
		{match: "bwrap: Setting up ", class: ExitClassRunnerFailure},
		{match: "bwrap: loopback", class: ExitClassRunnerFailure},
		{match: "bwrap: Creating new namespace", class: ExitClassRunnerFailure},
	},
	"seatbelt": {
		{match: "sandbox-exec: ", class: ExitClassRunnerFailure},
		{match: "profile error", class: ExitClassRunnerFailure},
		{match: "sandbox_apply", class: ExitClassRunnerFailure},
	},
	"landlock": {
		{match: "landlock helper: ", class: ExitClassRunnerFailure},
		{match: "landlock: ", class: ExitClassRunnerFailure},
	},
}

// denialSignatures are the ways each backend's kernel refuses a write. A
// refusal means confinement worked.
var denialSignatures = map[string][]signature{
	"bubblewrap": {
		{match: "Read-only file system", class: ExitClassDenial},
		{match: "Permission denied", class: ExitClassDenial},
	},
	"seatbelt": {
		{match: "Operation not permitted", class: ExitClassDenial},
		{match: "Permission denied", class: ExitClassDenial},
	},
	"landlock": {
		{match: "Permission denied", class: ExitClassDenial},
		{match: "Read-only file system", class: ExitClassDenial},
	},
}

// runnerFailureExitCodes are reserved exit codes that identify a runner
// failure on their own, checked alongside the text. 126 is the shell
// convention for "found but not executable", which is what the Landlock
// helper exits with when it cannot apply its policy or exec the target.
var runnerFailureExitCodes = map[string][]int{
	"landlock": {126},
}

// ClassifyExit decides what a confined command's non-zero exit means. stderr
// is the combined diagnostic output the command produced; exitCode is its
// status.
//
// Runner failure is checked before denial, per the DSH discipline: a runner
// failure means the command never ran, while a denial means confinement
// worked. Confusing the two would make the model escalate around a broken
// sandbox. backendName selects the signature tables; an unknown backend gets
// no classification at all rather than a borrowed one.
func ClassifyExit(backendName string, exitCode int, stderr string) ExitDiagnosis {
	normalized := strings.TrimSpace(stderr)
	runnerSigs, ok := runnerFailureSignatures[backendName]
	if !ok {
		return ExitDiagnosis{
			Class:    ExitClassOrdinary,
			Backend:  backendName,
			ExitCode: exitCode,
			Detail:   firstSignatureLine(normalized),
		}
	}

	// A reserved exit code identifies a runner failure on its own, before
	// any text is inspected.
	for _, code := range runnerFailureExitCodes[backendName] {
		if exitCode == code {
			return ExitDiagnosis{
				Class:    ExitClassRunnerFailure,
				Backend:  backendName,
				ExitCode: exitCode,
				Detail:   firstSignatureLine(normalized),
			}
		}
	}

	// Runner-failure signatures are checked first: a failed boundary means
	// the command never ran.
	for _, sig := range runnerSigs {
		if strings.Contains(normalized, sig.match) {
			return ExitDiagnosis{
				Class:    ExitClassRunnerFailure,
				Backend:  backendName,
				ExitCode: exitCode,
				Detail:   matchedLine(normalized, sig.match),
			}
		}
	}

	// Then denial signatures: confinement worked and refused something.
	for _, sig := range denialSignatures[backendName] {
		if strings.Contains(normalized, sig.match) {
			return ExitDiagnosis{
				Class:    ExitClassDenial,
				Backend:  backendName,
				ExitCode: exitCode,
				Detail:   matchedLine(normalized, sig.match),
			}
		}
	}

	return ExitDiagnosis{
		Class:    ExitClassOrdinary,
		Backend:  backendName,
		ExitCode: exitCode,
		Detail:   firstSignatureLine(normalized),
	}
}

// firstSignatureLine returns the first non-empty line of the output, for
// diagnostics that carry no recognizable signature.
func firstSignatureLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// matchedLine returns the first line containing the matched signature.
func matchedLine(output, match string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, match) {
			return strings.TrimSpace(line)
		}
	}
	return firstSignatureLine(output)
}

// ModelMessage renders the model-facing text for a diagnosis. Denials and
// runner failures get distinct, actionable texts; ordinary exits return an
// empty string because the command's own output already tells the story.
func (d ExitDiagnosis) ModelMessage() string {
	switch d.Class {
	case ExitClassRunnerFailure:
		return fmt.Sprintf(
			"sandbox-runner-failed backend=%s detail=%s hint=the sandbox itself failed to start the command; report this to the user rather than retrying or requesting sandbox escalation",
			d.Backend, d.Detail)
	case ExitClassDenial:
		return fmt.Sprintf(
			"sandbox-denied backend=%s detail=%s hint=the sandbox refused this operation; check which paths are writable with crush sandbox status",
			d.Backend, d.Detail)
	default:
		return ""
	}
}
