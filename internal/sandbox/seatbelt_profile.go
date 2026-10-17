package sandbox

import (
	"errors"
	"fmt"
	"strings"
)

// SeatbeltProfile generates the SBPL profile for a policy. The profile allows
// reads everywhere (reads are deliberately unconfined) and writes only under
// the policy's writable roots, then appends deny rules for protected paths and
// write denials so the last match wins.
//
// It is exported so the generated text can be characterized by golden tests.
func SeatbeltProfile(p *Policy) (string, error) {
	if p == nil {
		return "", errors.New("seatbelt: nil policy")
	}

	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(allow default)\n")
	// Reads are unconfined in every mode; state it explicitly rather than
	// relying on (allow default).
	b.WriteString("(allow file-read*)\n")

	// Deny all writes, then re-allow the writable roots. A root list rather
	// than a single root is the deliberate departure from the DSH model.
	b.WriteString("(deny file-write*)\n")
	for _, root := range append(p.WriteRootsFor(), "/dev/null") {
		b.WriteString(seatbeltAllowWrite(root))
	}

	// Protected paths and configured denials run last, so the last match
	// wins and they cannot be re-allowed by a writable root.
	for _, prot := range p.Protected {
		b.WriteString(seatbeltDenyWrite(prot))
	}
	for _, deny := range p.DenyWrites {
		b.WriteString(seatbeltDenyWrite(deny))
	}

	return b.String(), nil
}

// seatbeltRulePath canonicalizes a path for the profile: the profile must name
// real paths, so a root reached through a symlinked path still matches.
func seatbeltRulePath(path string) string {
	canonical := CanonicalPath(path)
	// SBPL strings are quoted; backslashes and double quotes are the only
	// characters that need escaping for the paths Crush generates.
	canonical = strings.ReplaceAll(canonical, `\`, `\\`)
	return strings.ReplaceAll(canonical, `"`, `\"`)
}

func seatbeltAllowWrite(root string) string {
	return fmt.Sprintf("(allow file-write* (subpath \"%s\"))\n", seatbeltRulePath(root))
}

func seatbeltDenyWrite(path string) string {
	return fmt.Sprintf("(deny file-write* (subpath \"%s\"))\n", seatbeltRulePath(path))
}
