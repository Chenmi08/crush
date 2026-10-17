package tools

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// isReadOnlyInvocation reports whether cmd is a single, plain command
// invocation with nothing attached that could write to the filesystem, run a
// nested shell, or chain a second command.
//
// The read-only shortcut only decides whether a command needs a prompt, so it
// must be conservative: any construct it fails to understand makes the command
// ineligible. Redirects of every form are rejected — including input
// redirects, which can create a file when the target does not exist — as are
// pipes, `&&`/`||`, `;` and newlines, background jobs, subshells, command
// substitution, process substitution, and compound commands.
//
// The command must also parse: a string the embedded shell cannot parse can
// never run, but treating it as read-only would be misleading, so it is not
// eligible.
func isReadOnlyInvocation(cmd string) bool {
	file, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		return false
	}
	if len(file.Stmts) != 1 {
		return false
	}

	stmt := file.Stmts[0]
	if stmt.Negated || stmt.Background || stmt.Coprocess {
		return false
	}

	// Only a bare command invocation qualifies. Compound statements (if,
	// for, while, case, blocks, subshells, functions, arithmetic commands)
	// all either chain commands or run in a nested environment.
	if _, ok := stmt.Cmd.(*syntax.CallExpr); !ok {
		return false
	}

	eligible := true
	syntax.Walk(stmt, func(node syntax.Node) bool {
		if !eligible {
			return false
		}
		switch n := node.(type) {
		case *syntax.Redirect:
			// Every redirect form is a write intent: `>` and `>>` write,
			// `<` and here-docs can create, and `&>`/`>&` dup onto them.
			eligible = false
		case *syntax.CmdSubst:
			// Command substitution runs an arbitrary command.
			eligible = false
		case *syntax.ProcSubst:
			// Process substitution also runs an arbitrary command.
			eligible = false
		case *syntax.BinaryCmd:
			// Pipes, `&&`, and `||`.
			eligible = false
		case *syntax.IfClause, *syntax.ForClause, *syntax.WhileClause,
			*syntax.CaseClause, *syntax.Block, *syntax.Subshell,
			*syntax.FuncDecl, *syntax.ArithmCmd, *syntax.TestClause,
			*syntax.DeclClause, *syntax.LetClause, *syntax.TimeClause:
			eligible = false
		case *syntax.Word:
			// A word made only of literals and parameter expansions cannot
			// write. Anything else (command substitution, process
			// substitution, arithmetic with side effects) is caught above
			// or below.
			for _, part := range n.Parts {
				switch part.(type) {
				case *syntax.Lit, *syntax.SglQuoted, *syntax.DblQuoted,
					*syntax.ParamExp, *syntax.ArithmExp:
				default:
					eligible = false
				}
			}
		}
		return true
	})
	return eligible
}
