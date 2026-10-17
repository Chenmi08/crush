package sandbox

import "strings"

// IsSandboxExcludedCommand reports whether a shell command matches a configured
// exclusion. Exclusions are matched against the command text as written, not
// against a wrapped or interpreted command, so `docker *` does not match
// `bash -c 'docker run'`: an interpreter or wrapper must be excluded
// explicitly, because excluding it widens scope to whatever it runs.
func IsSandboxExcludedCommand(command string, patterns []string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	for _, pattern := range patterns {
		if matchExclusion(command, strings.TrimSpace(pattern)) {
			return true
		}
	}
	return false
}

// matchExclusion matches one pattern. A pattern without a wildcard is an exact
// command-name match (or the full command text); a pattern with one wildcard
// is a prefix/suffix match.
func matchExclusion(command, pattern string) bool {
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "*") {
		if command == pattern {
			return true
		}
		fields := strings.Fields(command)
		return len(fields) > 0 && fields[0] == pattern
	}
	prefix, suffix, _ := strings.Cut(pattern, "*")
	if suffix == "" {
		return strings.HasPrefix(command, prefix)
	}
	if prefix == "" {
		return strings.HasSuffix(command, suffix)
	}
	return strings.HasPrefix(command, prefix) && strings.HasSuffix(command, suffix)
}
