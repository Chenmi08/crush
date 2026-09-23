package tools

import (
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// dangerousEntry is one configured dangerous command pattern: an ordered
// command-name prefix plus a required set of option tokens.
type dangerousEntry struct {
	// Name holds the command-name tokens, matched against the command's
	// leading tokens in order (e.g. "git branch").
	name []string
	// Flags holds the option tokens that must all be present among the
	// command's arguments, order-independent (e.g. "-d").
	flags []string
}

// IsDangerousCommand reports whether the shell command matches any entry in
// the configured dangerous-command list. An empty list never matches.
//
// The command is parsed with the repo's own shell parser so quoting, pipes,
// `&&`, `;`, and command substitution are handled correctly. Each command
// invocation is matched in two parts: the entry's command-name tokens must
// match the invocation's leading tokens in order, and the entry's option
// tokens must all be present among the invocation's arguments (operands are
// ignored). Combined short flags are expanded on both sides, so an entry of
// "-rf" also matches "-fr" or "-r -f".
//
// A command that fails to parse can't execute through the embedded shell,
// which uses the same parser, so it is never treated as dangerous.
func isDangerousCommand(cmd string, configured []string) bool {
	if len(configured) == 0 {
		return false
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		return false
	}
	entries := parseDangerousEntries(configured)
	dangerous := false
	syntax.Walk(file, func(node syntax.Node) bool {
		if ce, ok := node.(*syntax.CallExpr); ok && callMatchesDangerous(ce, entries) {
			dangerous = true
		}
		return true
	})
	return dangerous
}

// ParseDangerousEntries splits each configured pattern into its command-name
// and option-token parts. The command name runs up to the first token that
// starts with "-"; everything from there on is a required option.
func parseDangerousEntries(configured []string) []dangerousEntry {
	entries := make([]dangerousEntry, 0, len(configured))
	for _, pattern := range configured {
		tokens := strings.Fields(pattern)
		if len(tokens) == 0 {
			continue
		}
		i := 0
		for i < len(tokens) && !strings.HasPrefix(tokens[i], "-") {
			i++
		}
		entries = append(entries, dangerousEntry{
			name:  tokens[:i],
			flags: tokens[i:],
		})
	}
	return entries
}

// dangerousWrappers are commands that execute a wrapped command (e.g. rtk
// <cmd>) while renaming the command name. A rewrite such as `rtk git push`
// must still match a `git push` dangerous entry, so the wrapper is stripped
// before matching.
var dangerousWrappers = []string{"rtk"}

// CallMatchesDangerous reports whether the command invocation matches any
// dangerous entry. A leading wrapper command (e.g. rtk) is stripped first so
// a rewritten invocation still matches the wrapped command's entry.
func callMatchesDangerous(ce *syntax.CallExpr, entries []dangerousEntry) bool {
	tokens := make([]string, 0, len(ce.Args))
	for _, w := range ce.Args {
		tokens = append(tokens, wordLiteral(w))
	}
	if len(tokens) > 1 && slices.Contains(dangerousWrappers, tokens[0]) {
		tokens = tokens[1:]
	}
	for _, entry := range entries {
		if !prefixEqual(tokens, entry.name) {
			continue
		}
		if entry.flagsPresent(tokens[len(entry.name):]) {
			return true
		}
	}
	return false
}

// FlagsPresent reports whether every required option token appears among the
// command's remaining argument tokens, order-independent and with combined
// short flags expanded to their single-flag set.
func (e dangerousEntry) flagsPresent(args []string) bool {
	have := flagSet(args)
	want := flagSet(e.flags)
	for flag := range want {
		if !have[flag] {
			return false
		}
	}
	return true
}

// FlagSet expands each option token into its normalized flag names and
// returns them as a set. Long options (--foo) are kept verbatim; combined
// short options (-rf) are split into single-letter flags (-r, -f). Operands
// and "-" are ignored.
func flagSet(tokens []string) map[string]bool {
	set := make(map[string]bool)
	for _, t := range tokens {
		switch {
		case strings.HasPrefix(t, "--"):
			set[t] = true
		case strings.HasPrefix(t, "-") && len(t) > 1:
			body := t[1:]
			if len(body) == 1 {
				set[t] = true
				continue
			}
			for _, r := range body {
				set["-"+string(r)] = true
			}
		}
	}
	return set
}

// PrefixEqual reports whether tokens starts with prefix, in order.
func prefixEqual(tokens, prefix []string) bool {
	if len(tokens) < len(prefix) {
		return false
	}
	for i := range prefix {
		if tokens[i] != prefix[i] {
			return false
		}
	}
	return true
}

// WordLiteral returns the literal text of a word, concatenating its literal,
// single-quoted, and double-quoted parts. Expansions ($var, $(...))
// contribute nothing and are ignored, so a word made up entirely of
// expansions yields "".
func wordLiteral(w *syntax.Word) string {
	var b strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
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
