package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

// sandboxDocument is the subset of a config document needed to check sandbox
// layering.
type sandboxDocument struct {
	Permissions struct {
		Sandbox *Sandbox `json:"sandbox"`
	} `json:"permissions"`
}

// checkSandboxLayerOrder enforces "project configuration may tighten but never
// widen" across the documents the loader is about to merge. documents is in
// load order (lowest priority first), so the user-level documents come before
// the repository-local ones.
//
// A widening key in a later document is rejected with the offending key named.
// The check is deliberately conservative: it reports rather than silently
// ignoring, so a repository cannot quietly widen its own sandbox.
func checkSandboxLayerOrder(documents [][]byte) error {
	var base *Sandbox
	var baselineSet bool
	for _, doc := range documents {
		var parsed sandboxDocument
		if err := json.Unmarshal(doc, &parsed); err != nil {
			// Invalid JSON is reported by the loader with a better message.
			continue
		}
		box := parsed.Permissions.Sandbox
		if box == nil {
			continue
		}
		if !baselineSet {
			// The first document carrying sandbox settings establishes the
			// user's intent. Later documents are compared against it.
			copied := *box
			base = &copied
			baselineSet = true
			continue
		}
		if err := CheckProjectSandboxTightens(*box, *base); err != nil {
			slog.Warn("Project configuration attempted to widen the sandbox", "error", err)
			return fmt.Errorf("invalid sandbox configuration layer: %w", err)
		}
	}
	return nil
}

// IsZero reports whether the settings are entirely unset.
func (s Sandbox) IsZero() bool {
	return s.Mode == "" && len(s.AllowWrite) == 0 && len(s.DenyWrite) == 0 &&
		len(s.ExcludeCommands) == 0 && !s.FailIfUnavailable
}

// mergeSandboxLayers merges the sandbox settings from every document in load
// order, applying the tighten-only rule to everything after the first.
func mergeSandboxLayers(documents [][]byte) (Sandbox, error) {
	var result Sandbox
	var baselineSet bool
	for _, doc := range documents {
		var parsed sandboxDocument
		if err := json.Unmarshal(doc, &parsed); err != nil {
			continue
		}
		box := parsed.Permissions.Sandbox
		if box == nil {
			continue
		}
		if !baselineSet {
			result = *box
			baselineSet = true
			continue
		}
		if err := CheckProjectSandboxTightens(*box, result); err != nil {
			return Sandbox{}, err
		}
		result = mergeSandboxTighten(result, *box)
	}
	return result, nil
}
