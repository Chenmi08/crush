package tools

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsReadOnlyInvocation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		// Plain invocations keep the shortcut.
		{"plain ls", "ls -la", true},
		{"plain echo", "echo hello world", true},
		{"plain pwd", "pwd", true},
		{"plain git status", "git status", true},
		{"plain git log", "git log --oneline", true},
		{"argument with dollar sign", "echo $HOME", true},
		{"single quoted argument", "echo 'hello world'", true},
		{"double quoted argument", `echo "hello world"`, true},

		// Output redirects of every form are write intent.
		{"output redirect", "ls > /tmp/out", false},
		{"append redirect", "ls >> /tmp/out", false},
		{"stderr redirect", "ls 2> /tmp/err", false},
		{"append stderr redirect", "ls 2>> /tmp/err", false},
		{"combined redirect", "ls &> /tmp/out", false},
		{"reverse combined redirect", "ls >& /tmp/out", false},
		{"input redirect", "ls < /dev/null", false},
		{"read write redirect", "ls <> /tmp/out", false},
		{"here-doc", "cat <<EOF\nhello\nEOF", false},
		{"here-string", "cat <<< hello", false},
		{"heredoc without command chain", "ls <<EOF\nfoo\nEOF", false},

		// Chaining of every form.
		{"pipe", "ls | grep foo", false},
		{"pipe into pager free reader", "git log | cat", false},
		{"double ampersand", "ls && echo done", false},
		{"double pipe", "ls || echo fail", false},
		{"semicolon", "ls; echo done", false},
		{"newline", "ls\necho done", false},
		{"background job", "ls & echo done", false},
		{"subshell", "(ls)", false},
		{"backtick substitution", "ls `echo foo`", false},
		{"dollar substitution", "ls $(echo foo)", false},
		{"process substitution", "ls <(echo foo)", false},
		{"negated command", "! ls", false},
		{"for loop", "for f in *; do echo $f; done", false},
		{"if clause", "if true; then ls; fi", false},
		{"while clause", "while true; do ls; done", false},
		{"brace block", "{ ls; }", false},
		{"function declaration", "f() { ls; }", false},
		{"arithmetic command", "(( 1 + 1 ))", false},
		{"declaration", "declare foo=bar", false},

		// Unparseable input is never eligible.
		{"unbalanced quote", "echo 'unterminated", false},
		{"empty string", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, isReadOnlyInvocation(tt.input),
				"isReadOnlyInvocation(%q)", tt.input)
		})
	}
}

func TestIsSafeReadOnlyCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		dangerous bool
		expected  bool
	}{
		{"safe plain", "git status", false, true},
		{"safe with flag", "git log --oneline", false, true},
		{"safe prefix boundary", "lsfoo", false, false},
		{"unsafe command", "rm -rf /", false, false},
		{"safe but dangerous", "git branch -D feature", true, false},
		{"safe with output redirect", "git status > ~/.bashrc", false, false},
		{"safe with append redirect", "git status >> ~/.bashrc", false, false},
		{"safe with chained command", "git status && rm -rf /", false, false},
		{"safe with pipe", "git log | head", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, isSafeReadOnlyCommand(tt.input, tt.dangerous),
				"isSafeReadOnlyCommand(%q, %v)", tt.input, tt.dangerous)
		})
	}
}
