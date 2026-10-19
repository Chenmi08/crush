package tools

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// defaultNetworkCommands lists the shell commands that change state outside
// the machine: remote mutations and privilege escalation. The sandbox
// confines writes but not network access, so these are the operations it
// cannot fence and therefore the ones that still prompt. Everything else
// runs confined and auto-approved, and danger-full-access delegates fully.
//
// The list is deliberately a token-prefix match, not a boundary: any
// interpreter (python -c, go run) can reach the network. A miss costs one
// unprompted but still confined call, which is an acceptable trade for not
// maintaining a language of execution semantics. Read-only egress (curl,
// wget, nc, telnet) is not listed: it changes nothing remotely, and its
// local writes are already fenced, so prompting for it would only add noise
// without adding a boundary.
var defaultNetworkCommands = []string{
	// Remote services that act with the user's credentials.
	"ssh",
	"scp",
	"sftp",
	"rsync",
	"ftp",
	"gh",

	// Git's remote-mutating surfaces.
	"git push",

	// Privilege escalation is a capability escape, not a write.
	"sudo",
	"su",
	"doas",
}

// wrapperCommands are commands that prepend another command without changing
// what runs. A wrapper is transparent: `env curl`, `nohup ssh`, and `timeout
// 5 curl` all reach the wrapped command's egress, so matching skips over
// these names while collecting a call's leading tokens. The list is a fixed
// set, not a general executor pass-through: a name not listed here stops the
// walk and the command does not match, because looking through an arbitrary
// executor (xargs, bash) widens the match to commands it might run.
var wrapperCommands = map[string]bool{
	"command": true,
	"env":     true,
	"nice":    true,
	"nohup":   true,
	"setsid":  true,
	"stdbuf":  true,
	"strace":  true,
	"time":    true,
	"timeout": true,
}

// valueArgWrappers take a value before the wrapped command, so one extra
// token is skipped after the wrapper name (timeout 5 curl, nice -n 10 curl).
var valueArgWrappers = map[string]bool{
	"nice":    true,
	"timeout": true,
}

// isNetworkCommand reports whether any command invocation in the command
// line — including piped, chained, and subshelled ones — matches an entry in
// the default network list or the user's configured additions, looking
// through the known transparent wrappers.
//
// The command is parsed with the same shell parser the embedded shell uses,
// so quoting, pipes, &&, ;, and command substitution are handled correctly.
// A command that fails to parse can never run through the shell, so it never
// matches. Each invocation is matched by its leading literal tokens: a
// single-token entry matches the command name ("curl ..."), a multi-token
// entry matches the invocation's leading tokens in order ("git push origin
// main" matches "git push").
func isNetworkCommand(cmd string, extra []string) bool {
	entries := defaultNetworkCommands
	if len(extra) > 0 {
		entries = make([]string, 0, len(defaultNetworkCommands)+len(extra))
		entries = append(entries, defaultNetworkCommands...)
		entries = append(entries, extra...)
	}

	file, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		return false
	}
	network := false
	syntax.Walk(file, func(node syntax.Node) bool {
		if network {
			return false
		}
		if ce, ok := node.(*syntax.CallExpr); ok && callMatchesNetwork(ce, entries) {
			network = true
			return false
		}
		return true
	})
	return network
}

// callMatchesNetwork reports whether one invocation's leading literal tokens
// match a network entry. Transparent wrapper tokens are skipped, so `env
// curl` and `timeout 5 curl` still match "curl"; the wrapper's own value
// argument (`5`, `-n`) is skipped with it. An expansion at any position
// makes the rest of the prefix unknowable and ends token collection for
// that invocation.
func callMatchesNetwork(ce *syntax.CallExpr, entries []string) bool {
	// cmdTokens are the invocation's tokens with transparent wrappers and
	// their value arguments removed, so the entries match against the
	// wrapped command.
	var cmdTokens []string
	// skipValue is set right after collecting a value-taking wrapper's
	// name: the next token is that wrapper's value, not part of the
	// command.
	skipValue := false
	for _, w := range ce.Args {
		lit := wordLiteral(w)
		if lit == "" {
			// An expansion or a word that contributes no literal text
			// makes the prefix unknowable; stop collecting.
			break
		}
		if skipValue {
			// Skip the wrapper's option and value tokens: `-n` of
			// `nice -n 10`, the `5` of `timeout 5`, and their kin. The
			// first token that is neither a flag nor a number ends the
			// value section and is the wrapped command.
			if strings.HasPrefix(lit, "-") || isAllDigits(lit) {
				continue
			}
			skipValue = false
		}
		if wrapperCommands[lit] {
			// Transparent wrapper: hide it from the command tokens.
			if valueArgWrappers[lit] {
				skipValue = true
			}
			continue
		}
		cmdTokens = append(cmdTokens, lit)
	}

	for _, entry := range entries {
		prefix := strings.Fields(entry)
		if len(prefix) == 0 || len(cmdTokens) < len(prefix) {
			continue
		}
		match := true
		for i, token := range prefix {
			if cmdTokens[i] != token {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// wordLiteral returns the literal text of a word, concatenating its literal
// and quoted parts. The parser keeps backslash escapes in literal text, so
// `\curl` arrives as `\curl`; unescapeBackslashes restores the command
// name. Expansions ($var, $(...)) contribute nothing, so a word made up
// entirely of expansions yields "".
func wordLiteral(w *syntax.Word) string {
	var b strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(unescapeBackslashes(p.Value))
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, inner := range p.Parts {
				if lit, ok := inner.(*syntax.Lit); ok {
					b.WriteString(lit.Value)
				}
			}
		}
	}
	return b.String()
}

// unescapeBackslashes strips a backslash that escapes the following
// character (`\curl` → `curl`), so an escaped command name still matches.
// A trailing backslash is kept as written; it cannot begin a command name
// this matcher would care about.
func unescapeBackslashes(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// isAllDigits reports whether the token is a plain non-negative integer,
// the shape of a wrapper's numeric value argument.
func isAllDigits(token string) bool {
	if token == "" {
		return false
	}
	for _, r := range token {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
