package sandbox

import "strings"

// normalizeRoots replaces a temp root in generated backend text so golden
// files are stable across runs.
func normalizeRoots(text, root string) string {
	return strings.ReplaceAll(text, root, "<ROOT>")
}
