package tools

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsNetworkCommand_Direct(t *testing.T) {
	t.Parallel()

	require.True(t, isNetworkCommand("curl https://example.com", nil))
	require.True(t, isNetworkCommand("curl", nil), "a bare command name matches")
	require.True(t, isNetworkCommand("git push origin main", nil),
		"multi-token entries match the leading tokens in order")
	require.True(t, isNetworkCommand("git remote -v", nil))
	require.True(t, isNetworkCommand("sudo apt install jq", nil))
	require.True(t, isNetworkCommand("ssh host", nil))
	require.True(t, isNetworkCommand("gh pr create", nil))
}

func TestIsNetworkCommand_NonMatches(t *testing.T) {
	t.Parallel()

	require.False(t, isNetworkCommand("git pushdry", nil), "tokens must match whole")
	require.False(t, isNetworkCommand("ls -la", nil))
	require.False(t, isNetworkCommand("git status && git log", nil))
	require.False(t, isNetworkCommand("go test ./...", nil))
	require.False(t, isNetworkCommand("", nil))
}

func TestIsNetworkCommand_PipesAndChains(t *testing.T) {
	t.Parallel()

	require.True(t, isNetworkCommand("ls && curl https://example.com", nil),
		"a chained network command matches")
	require.True(t, isNetworkCommand("echo hi | nc host 1234", nil),
		"a piped network command matches")
	require.True(t, isNetworkCommand("ls; ssh host", nil),
		"a sequenced network command matches")
	require.True(t, isNetworkCommand("echo $(curl https://example.com)", nil),
		"a network command in a substitution matches")
	require.True(t, isNetworkCommand("git add . || git push", nil),
		"an or-chained network command matches")
}

func TestIsNetworkCommand_Wrappers(t *testing.T) {
	t.Parallel()

	require.True(t, isNetworkCommand("env curl https://example.com", nil),
		"env is transparent")
	require.True(t, isNetworkCommand("nohup ssh host", nil),
		"nohup is transparent")
	require.True(t, isNetworkCommand("timeout 5 curl https://example.com", nil),
		"timeout's value argument is skipped")
	require.True(t, isNetworkCommand("nice -n 10 ssh host", nil),
		"nice's value argument is skipped")
	require.True(t, isNetworkCommand("command git push", nil))

	require.False(t, isNetworkCommand("xargs curl", nil),
		"an unlisted executor is not looked through")
	require.False(t, isNetworkCommand("env | grep ssh", nil),
		"env with no wrapped command does not match")
}

func TestIsNetworkCommand_Quoting(t *testing.T) {
	t.Parallel()

	require.True(t, isNetworkCommand("'curl' https://example.com", nil),
		"a quoted command name matches")
	require.True(t, isNetworkCommand("\"git\" push origin", nil),
		"a double-quoted command name matches")
	require.True(t, isNetworkCommand("\\curl https://example.com", nil),
		"an escaped command name matches")
	require.False(t, isNetworkCommand("$cmd", nil),
		"an expansion-only word cannot match")
}

func TestIsNetworkCommand_Extras(t *testing.T) {
	t.Parallel()

	require.True(t, isNetworkCommand("docker push repo/img", []string{"docker push"}),
		"configured additions join the built-in list")
	require.True(t, isNetworkCommand("docker push", []string{"docker push"}))
	require.False(t, isNetworkCommand("docker images", []string{"docker push"}),
		"only the configured prefix matches")
	require.True(t, isNetworkCommand("curl https://example.com", []string{}),
		"an empty addition list keeps the built-in entries")
}

func TestIsNetworkCommand_SubshellAndBackground(t *testing.T) {
	t.Parallel()

	require.True(t, isNetworkCommand("(curl https://example.com)", nil),
		"a subshelled network command matches")
	require.True(t, isNetworkCommand("curl https://example.com & echo done", nil),
		"a backgrounded network command matches")
	require.True(t, isNetworkCommand("if true; then ssh host; fi", nil),
		"a network command in a compound statement matches")
}
