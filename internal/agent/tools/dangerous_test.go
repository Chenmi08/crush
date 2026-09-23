package tools

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsDangerousCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		command   string
		config    []string
		dangerous bool
	}{
		{"empty config never matches", "rm -rf /", nil, false},
		{"rm matches any invocation", "rm -rf /", []string{"rm"}, true},
		{"rm matches plain file removal", "rm file", []string{"rm"}, true},
		{"rm -rf matches flags", "rm -rf /", []string{"rm -rf"}, true},
		{"rm -rf matches quoted flags", `rm "-rf" /`, []string{"rm -rf"}, true},
		{"git branch -d matches quoted flag", `git branch "-d" foo`, []string{"git branch -d"}, true},
		{"rm -rf matches flags after operand", "rm / -rf", []string{"rm -rf"}, true},
		{"rm -rf matches split flags", "rm -r -f /", []string{"rm -rf"}, true},
		{"rm -rf matches combined reversed", "rm -fr /", []string{"rm -rf"}, true},
		{"rm -rf does not match plain removal", "rm file", []string{"rm -rf"}, false},
		{"mv with -f matches", "mv -f a b", []string{"mv -f"}, true},
		{"mv without -f does not match", "mv a b", []string{"mv -f"}, false},
		{"git branch matches all branch ops", "git branch -D foo", []string{"git branch"}, true},
		{"git branch -d matches delete", "git branch -d foo", []string{"git branch -d"}, true},
		{"git branch -d ignores flag order", "git branch foo -d", []string{"git branch -d"}, true},
		{"git branch -d does not match -D", "git branch -D foo", []string{"git branch -d"}, false},
		{"git branch -d does not match listing", "git branch --list", []string{"git branch -d"}, false},
		{"git push matches", "git push origin main", []string{"git push"}, true},
		{"git push --force matches", "git push --force origin main", []string{"git push --force"}, true},
		{"git push --force does not match plain push", "git push origin main", []string{"git push --force"}, false},
		{"command buried in chain matches", "echo hi && rm -rf /", []string{"rm -rf"}, true},
		{"command in substitution matches", "$(git clean -fdx)", []string{"git clean"}, true},
		{"command in backtick substitution matches", "`git clean -fdx`", []string{"git clean"}, true},
		{"rtk wrapper is stripped for git push", "rtk git push", []string{"git push"}, true},
		{"rtk wrapper is stripped for git branch -D", "rtk git branch -D foo", []string{"git branch -D"}, true},
		{"rtk wrapper does not match unrelated entries", "rtk ls -la", []string{"git push"}, false},
		{"quoted destructive text is safe", `echo "rm -rf /"`, []string{"rm -rf"}, false},
		{"unparseable command is not dangerous", "rm -rf |", []string{"rm -rf"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isDangerousCommand(tt.command, tt.config)
			require.Equal(t, tt.dangerous, got)
		})
	}
}
