package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
)

// sandboxDocument is the subset of a config document needed to check sandbox
// layering.
type sandboxDocument struct {
	Permissions struct {
		Sandbox *Sandbox `json:"sandbox"`
	} `json:"permissions"`
}

// sandboxLayer is one config document plus whether it is repository-local.
type sandboxLayer struct {
	document []byte
	project  bool
}

// checkSandboxLayerOrder enforces "project configuration may tighten but never
// widen" across the documents the loader is about to merge.
//
// The user-level documents are merged freely: they are all the user's own
// choice, and later files win. A repository-local document is compared against
// the user-level result and may only add denials and exclusions. A widening key
// is rejected with the offending key named, so a repository cannot quietly
// widen its own sandbox.
func checkSandboxLayerOrder(layers []sandboxLayer) error {
	var userLevel Sandbox
	seenUserLevel := false
	for _, layer := range layers {
		var parsed sandboxDocument
		if err := json.Unmarshal(layer.document, &parsed); err != nil {
			// Invalid JSON is reported by the loader with a better message.
			continue
		}
		box := parsed.Permissions.Sandbox
		if box == nil {
			continue
		}
		if !layer.project {
			userLevel = mergeSandboxUserLevel(userLevel, *box)
			seenUserLevel = true
			continue
		}
		if !seenUserLevel {
			// No user-level settings: a project may still only tighten the
			// defaults.
			userLevel = Sandbox{}
		}
		if err := CheckProjectSandboxTightens(*box, userLevel); err != nil {
			slog.Warn("Project configuration attempted to widen the sandbox", "error", err)
			return fmt.Errorf("invalid sandbox configuration layer: %w", err)
		}
	}
	return nil
}

// mergeSandboxUserLevel merges two user-level documents: a later file wins on
// scalars, and lists accumulate with the later file's values taking precedence.
func mergeSandboxUserLevel(base, next Sandbox) Sandbox {
	merged := next
	if merged.Mode == "" {
		merged.Mode = base.Mode
	}
	merged.AllowWrite = appendUnique(base.AllowWrite, next.AllowWrite...)
	merged.DenyWrite = appendUnique(base.DenyWrite, next.DenyWrite...)
	merged.ExcludeCommands = appendUnique(base.ExcludeCommands, next.ExcludeCommands...)
	merged.FailIfUnavailable = base.FailIfUnavailable || next.FailIfUnavailable
	return merged
}

// appendUnique appends values not already present.
func appendUnique(base []string, values ...string) []string {
	out := slices.Clone(base)
	for _, value := range values {
		if !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	return out
}

// IsZero reports whether the settings are entirely unset.
func (s Sandbox) IsZero() bool {
	return s.Mode == "" && len(s.AllowWrite) == 0 && len(s.DenyWrite) == 0 &&
		len(s.ExcludeCommands) == 0 && !s.FailIfUnavailable
}

// mergeSandboxLayers merges the sandbox settings from every document, applying
// the tighten-only rule to repository-local documents.
func mergeSandboxLayers(layers []sandboxLayer) (Sandbox, error) {
	var result Sandbox
	for _, layer := range layers {
		var parsed sandboxDocument
		if err := json.Unmarshal(layer.document, &parsed); err != nil {
			continue
		}
		box := parsed.Permissions.Sandbox
		if box == nil {
			continue
		}
		if !layer.project {
			result = mergeSandboxUserLevel(result, *box)
			continue
		}
		if err := CheckProjectSandboxTightens(*box, result); err != nil {
			return Sandbox{}, err
		}
		result = mergeSandboxTighten(result, *box)
	}
	return result, nil
}
