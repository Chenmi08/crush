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
	// Mode is the policy mode the command ran under. It is set by the exec
	// layer, which has the policy; a diagnosis produced without a policy (a
	// direct ClassifyExit call) leaves it empty.
	Mode Mode
}

func (d ExitDiagnosis) String() string {
	return fmt.Sprintf("sandbox-exit class=%s%s backend=%s exit=%d detail=%s", d.Class, d.modeSuffix(), d.Backend, d.ExitCode, d.Detail)
}

// modeSuffix returns the " mode=<mode>" fragment for messages and logs, or
// an empty string when no mode is known.
func (d ExitDiagnosis) modeSuffix() string {
	if d.Mode == "" {
		return ""
	}
	return " mode=" + string(d.Mode)
}

// escalationTargets renders the modes strictly wider than m for the denial
// hint. Only genuinely wider modes are named: naming a mode that is not
// wider would send the model into an escalation-not-wider rejection.
// read-only is never a target (no mode is narrower than it), and an unknown
// mode keeps the confined escalation candidates.
func escalationTargets(m Mode) string {
	var parts []string
	for _, candidate := range Modes() {
		if candidate == ModeReadOnly {
			continue
		}
		if m != "" && !candidate.wider(m) {
			continue
		}
		parts = append(parts, string(candidate))
	}
	if len(parts) == 1 {
		return "(" + parts[0] + ")"
	}
	return "(" + strings.Join(parts, ", or ") + ")"
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
//
// Only these signatures are trusted. A refused write to a path masked as an
// empty directory reports EISDIR instead, which is deliberately not
// classified: the text (`Is a directory`) is identical to an ordinary misuse
// such as `cat somedir`, so claiming "the sandbox refused this" would be
// wrong as often as it is right. The command's own error text still reaches
// the model.
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
		// Actionable: the model must know the refusal is the sandbox and
		// that the remedy is to ask for a wider mode for this one call,
		// otherwise it guesses (cd elsewhere, -C, retry) at a boundary that
		// cannot be crossed. Wording mirrors the escalation instructions in
		// the confined tools' descriptions so the two stay consistent.
		// The mode in effect is included when known, and only modes strictly
		// wider than it are named: escalation requires a strictly wider mode,
		// and a hint that names a non-wider one would send the model into an
		// escalation-not-wider rejection.
		return fmt.Sprintf(
			"sandbox-denied%s backend=%s detail=%s hint=the sandbox refused a write; if this operation is genuinely required, retry once with sandbox_permissions set to a mode strictly wider than the current one %s and a justification sentence; the user is asked to approve leaving the sandbox for that one call",
			d.modeSuffix(), d.Backend, d.Detail, escalationTargets(d.Mode))
	default:
		return ""
	}
}
