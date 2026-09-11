package config

import (
	"regexp"
	"strings"
)

// Runtime variables, resolved per request instead of at config load.
const (
	RuntimeVarSessionID   = "CRUSH_SESSION_ID"
	RuntimeVarSessionHash = "CRUSH_SESSION_HASH"
	RuntimeVarMessageID   = "CRUSH_MESSAGE_ID"
	RuntimeVarProjectID   = "CRUSH_PROJECT_ID"
)

var (
	runtimeVarNames  = []string{RuntimeVarSessionID, RuntimeVarSessionHash, RuntimeVarMessageID, RuntimeVarProjectID}
	runtimeBareVarRe = regexp.MustCompile(`\$(` + strings.Join(runtimeVarNames, "|") + `)\b`)
)

// protectRuntimeVars replaces runtime variable references with markers so
// shell expansion at config-load time leaves them alone.
func protectRuntimeVars(value string) (string, bool) {
	found := false
	for _, name := range runtimeVarNames {
		ref := "${" + name + "}"
		if strings.Contains(value, ref) {
			value = strings.ReplaceAll(value, ref, runtimeMarker(name))
			found = true
		}
	}
	value = runtimeBareVarRe.ReplaceAllStringFunc(value, func(match string) string {
		found = true
		return runtimeMarker(match[1:])
	})
	return value, found
}

func runtimeMarker(name string) string {
	return "{{" + name + "}}"
}

// ExpandRuntimeHeaders substitutes the markers left in runtime header
// values by config resolution. Headers that resolve to an empty string
// are dropped.
func ExpandRuntimeHeaders(headers map[string]string, vars map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for key, value := range headers {
		for name, val := range vars {
			value = strings.ReplaceAll(value, runtimeMarker(name), val)
		}
		if value == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
