package tools

import (
	"runtime"
	"strings"
)

var safeCommands = append([]string{
	// Bash builtins and core utils
	"cal",
	"date",
	"df",
	"du",
	"echo",
	"env",
	"free",
	"groups",
	"hostname",
	"id",
	"kill",
	"killall",
	"ls",
	"nice",
	"nohup",
	"printenv",
	"ps",
	"pwd",
	"set",
	"time",
	"timeout",
	"top",
	"type",
	"uname",
	"unset",
	"uptime",
	"whatis",
	"whereis",
	"which",
	"whoami",

	// Search tools. The coder's dedicated glob/grep/ls tools are excluded, so
	// it searches with rg/fd through bash; auto-approving them avoids a
	// prompt on every call. fd's exec flags are blocked in bash.go.
	"rg",

	// Git
	"git blame",
	"git branch",
	"git config --get",
	"git config --list",
	"git describe",
	"git diff",
	"git grep",
	"git log",
	"git ls-files",
	"git ls-remote",
	"git remote",
	"git rev-parse",
	"git shortlog",
	"git show",
	"git status",
	"git tag",
}, fdCommands...)

// isSafeReadOnlyCommand reports whether the command may run without a
// permission prompt: a configured dangerous command never qualifies, and the
// command must both match the read-only allow-list and be a plain invocation
// with no construct that can write or run something else (see
// [isReadOnlyInvocation]).
func isSafeReadOnlyCommand(cmd string, dangerous bool) bool {
	if dangerous {
		return false
	}
	if !isReadOnlyInvocation(cmd) {
		return false
	}
	cmdLower := strings.ToLower(strings.TrimSpace(cmd))
	for _, safe := range safeCommands {
		if strings.HasPrefix(cmdLower, safe) {
			if len(cmdLower) == len(safe) || cmdLower[len(safe)] == ' ' || cmdLower[len(safe)] == '-' {
				return true
			}
		}
	}
	return false
}

func init() {
	if runtime.GOOS == "windows" {
		safeCommands = append(
			safeCommands,
			// Windows-specific commands
			"ipconfig",
			"nslookup",
			"ping",
			"systeminfo",
			"tasklist",
			"where",
		)
	}
}
