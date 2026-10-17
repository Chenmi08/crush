package sandbox

import "log/slog"

// logSelection records the probed backend and enforcement level once, so a
// host can be diagnosed from the logs.
func logSelection(backend string, enforcement Enforcement, reason string) {
	if backend == "" {
		slog.Warn("No usable sandbox backend", "enforcement", enforcement, "reason", reason)
		return
	}
	slog.Info("Sandbox backend selected", "backend", backend, "enforcement", enforcement)
}

// LogDenial records a policy refusal in the logs. The count and the transient
// notice are published by the refusal itself (see Policy.refuse), so direct
// CheckWrite callers and tool-layer callers see identical streams.
func LogDenial(d Denial) {
	slog.Warn("Sandbox denied write",
		"path", d.Path,
		"mode", d.Mode,
		"protected", d.Protected,
	)
}

// slogInfo logs at info level. A tiny indirection so callers above do not each
// import slog.
func slogInfo(msg string, args ...any) {
	slog.Info(msg, args...)
}
