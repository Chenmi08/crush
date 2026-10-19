package tools

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsNetworkCommand_Direct(t *testing.T) {
	t.Parallel()

	require.True(t, isNetworkCommand("ssh host", nil))
	require.True(t, isNetworkCommand("ssh", nil), "a bare command name matches")
	require.True(t, isNetworkCommand("git push origin main", nil),
		"multi-token entries match the leading tokens in order")
	require.True(t, isNetworkCommand("sudo apt install jq", nil))
	require.True(t, isNetworkCommand("gh pr create", nil))
	require.True(t, isNetworkCommand("scp file host:/tmp", nil))
}

// TestIsNetworkCommand_ReadOnlyEgressDoesNotMatch pins that reads over the
// network are not a boundary the sandbox needs to prompt for: they mutate
// nothing remotely, and local writes are already fenced.
func TestIsNetworkCommand_ReadOnlyEgressDoesNotMatch(t *testing.T) {
	t.Parallel()

	require.False(t, isNetworkCommand("curl https://example.com", nil))
	require.False(t, isNetworkCommand("wget https://example.com/f", nil))
	require.False(t, isNetworkCommand("nc -z host 80", nil))
	require.False(t, isNetworkCommand("git remote -v", nil))
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

	require.True(t, isNetworkCommand("ls && ssh host", nil),
		"a chained mutating command matches")
	require.True(t, isNetworkCommand("ls; scp file host:/tmp", nil),
		"a sequenced mutating command matches")
	require.True(t, isNetworkCommand("git add . || git push", nil),
		"an or-chained mutating command matches")
	require.False(t, isNetworkCommand("echo $(curl https://example.com)", nil),
		"a read-only egress command does not match")
}

func TestIsNetworkCommand_Wrappers(t *testing.T) {
	t.Parallel()

	require.True(t, isNetworkCommand("env ssh host", nil),
		"env is transparent")
	require.True(t, isNetworkCommand("nohup ssh host", nil),
		"nohup is transparent")
	require.True(t, isNetworkCommand("timeout 5 ssh host", nil),
		"timeout's value argument is skipped")
	require.True(t, isNetworkCommand("nice -n 10 ssh host", nil),
		"nice's value argument is skipped")
	require.True(t, isNetworkCommand("command git push", nil))

	require.False(t, isNetworkCommand("xargs ssh", nil),
		"an unlisted executor is not looked through")
	require.False(t, isNetworkCommand("env | grep ssh", nil),
		"env with no wrapped command does not match")
}

func TestIsNetworkCommand_Quoting(t *testing.T) {
	t.Parallel()

	require.True(t, isNetworkCommand("'ssh' host", nil),
		"a quoted command name matches")
	require.True(t, isNetworkCommand("\"git\" push origin", nil),
		"a double-quoted command name matches")
	require.True(t, isNetworkCommand("\\ssh host", nil),
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
	require.True(t, isNetworkCommand("ssh host", []string{}),
		"an empty addition list keeps the built-in entries")
}

func TestIsNetworkCommand_SubshellAndBackground(t *testing.T) {
	t.Parallel()

	require.True(t, isNetworkCommand("(ssh host)", nil),
		"a subshelled mutating command matches")
	require.True(t, isNetworkCommand("ssh host & echo done", nil),
		"a backgrounded mutating command matches")
	require.True(t, isNetworkCommand("if true; then ssh host; fi", nil),
		"a network command in a compound statement matches")
}
