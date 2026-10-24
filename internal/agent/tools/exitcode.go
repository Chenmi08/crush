package tools

import (
	"regexp"
	"strconv"
)

// exitCodeLine matches the status line formatOutput appends when a command
// exits non-zero. It is the inverse of what bash.go writes, so it lives next
// to it rather than in a consumer.
var exitCodeLine = regexp.MustCompile(`(?m)^Exit code (\d+)$`)

// exitCodeScanBytes bounds how much of a tool result's tail is searched. The
// real status line is always last, so scanning the whole body would only make
// a quoted phrase in an earlier log more likely to be mistaken for it.
const exitCodeScanBytes = 512

// ParseExitCode reads the trailing `Exit code N` line that a bash result
// carries. It returns false when there is none, which is the normal case for
// a successful command and for results from other tools.
func ParseExitCode(content string) (int, bool) {
	tail := content
	if len(tail) > exitCodeScanBytes {
		tail = tail[len(tail)-exitCodeScanBytes:]
	}
	matches := exitCodeLine.FindAllStringSubmatch(tail, -1)
	if len(matches) == 0 {
		return 0, false
	}
	code, err := strconv.Atoi(matches[len(matches)-1][1])
	if err != nil {
		return 0, false
	}
	return code, true
}
