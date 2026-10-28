package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// manifestMsg builds a message for manifest tests.
func manifestMsg(id string, role message.MessageRole, at int64, parts ...message.ContentPart) message.Message {
	return message.Message{ID: id, Role: role, CreatedAt: at, Parts: parts}
}

func bashFailureMsg(id string, at int64, command, output string) message.Message {
	return manifestMsg(id, message.Assistant, at,
		message.ToolCall{ID: "call_" + id, Name: "bash", Input: `{"command":` + quoteJSON(command) + `}`},
	)
}

func bashResultMsg(id string, at int64, content string, isError bool) message.Message {
	return manifestMsg(id, message.Tool, at, message.ToolResult{
		ToolCallID: "call_" + strings.TrimSuffix(id, "-r"),
		Name:       "bash",
		Content:    content,
		IsError:    isError,
	})
}

func quoteJSON(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + replacer.Replace(s) + `"`
}

func TestBuildContextManifestEmpty(t *testing.T) {
	t.Parallel()

	require.Empty(t, buildContextManifest(manifestInput{}))
}

func TestBuildContextManifestFailuresNewestFirst(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		bashFailureMsg("err", 1000, "go build ./...", ""),
		bashResultMsg("err-r", 1001, "undefined: foo\n\nExit code 1", false),
		bashFailureMsg("flag", 2000, "go test ./internal/agent/...", ""),
		bashResultMsg("flag-r", 2001, "--- FAIL: TestSummarize (0.31s)\nFAIL\n\nExit code 1", true),
		// A successful command must not be listed.
		manifestMsg("ok", message.Assistant, 3000,
			message.ToolCall{ID: "call_ok", Name: "bash", Input: `{"command":"git status"}`},
		),
		manifestMsg("ok-r", message.Tool, 3001, message.ToolResult{
			ToolCallID: "call_ok", Name: "bash", Content: "clean\n",
		}),
	}

	block := buildContextManifest(manifestInput{Messages: msgs, AnchorAt: 4000})

	require.Contains(t, block, "<context_manifest")
	require.Contains(t, block, "</context_manifest>")
	require.Contains(t, block, "failures:")

	testIdx := strings.Index(block, `cmd="go test ./internal/agent/..."`)
	buildIdx := strings.Index(block, `cmd="go build ./..."`)
	require.GreaterOrEqual(t, testIdx, 0)
	require.GreaterOrEqual(t, buildIdx, 0)
	require.Less(t, testIdx, buildIdx, "newest failure must be listed first")

	require.NotContains(t, block, `cmd="git status"`)
	require.Contains(t, block, "exit=1")
	require.Contains(t, block, `key="--- FAIL: TestSummarize (0.31s)"`)
	require.Contains(t, block, `key="undefined: foo"`)
}

func TestBuildContextManifestIsErrorFailureWithoutExitCode(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		manifestMsg("view", message.Assistant, 1000,
			message.ToolCall{ID: "call_view", Name: "view", Input: `{"file_path":"nope.go"}`},
		),
		manifestMsg("view-r", message.Tool, 1001, message.ToolResult{
			ToolCallID: "call_view",
			Name:       "view",
			Content:    "Error: file not found",
			IsError:    true,
		}),
	}

	block := buildContextManifest(manifestInput{Messages: msgs, AnchorAt: 2000})

	require.Contains(t, block, "tool=view")
	require.NotContains(t, block, "exit=", "exit is bash-only")
	require.Contains(t, block, `key="Error: file not found"`)
}

func TestBuildContextManifestFilesDeduped(t *testing.T) {
	t.Parallel()

	block := buildContextManifest(manifestInput{
		Messages:      []message.Message{manifestMsg("u", message.User, 1000)},
		AnchorAt:      4000,
		WorktreeKnown: true,
		Files: []manifestFile{
			{Path: "internal/agent/agent.go", Last: 3000},
			{Path: "internal/message/message.go", Last: 1500},
			// A ledger row for the same path is merged, keeping the
			// newest timestamp rather than listing the file twice.
			{Path: "internal/agent/agent.go", Last: 3500},
		},
	})

	require.Contains(t, block, "files:")
	require.Equal(t, 1, strings.Count(block, "path=internal/agent/agent.go"),
		"a path is listed once, not once per ledger row")
	require.Contains(t, block, "path=internal/agent/agent.go last=")
	require.Contains(t, block, "src=tool")

	// The most recently written file comes first.
	agentIdx := strings.Index(block, "path=internal/agent/agent.go")
	messageIdx := strings.Index(block, "path=internal/message/message.go")
	require.Less(t, agentIdx, messageIdx)
}

func TestBuildContextManifestCoverageReportsUndecodable(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		manifestMsg("a", message.User, 1000, message.TextContent{Text: "hello"}),
		manifestMsg("b", message.User, 2000, message.TextContent{Text: "again"}),
	}

	clean := buildContextManifest(manifestInput{Messages: msgs, AnchorAt: 3000})
	require.Contains(t, clean, "coverage: 2/2 archived messages scanned")
	require.NotContains(t, clean, "undecodable")

	partial := buildContextManifest(manifestInput{Messages: msgs, AnchorAt: 3000, Undecodable: 2})
	require.Contains(t, partial, "coverage: 2/4 archived messages scanned (2 undecodable)")
}

func TestBuildContextManifestEscapesValues(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		bashFailureMsg("f", 1000, "echo '<a> & <b>' | tee out.txt", ""),
		bashResultMsg("f-r", 1001, "line one\nError: a < b & c > d\n\nExit code 1", true),
	}

	block := buildContextManifest(manifestInput{Messages: msgs, AnchorAt: 2000})

	require.Contains(t, block, "&lt;a&gt; &amp; &lt;b&gt;")
	require.Contains(t, block, "a &lt; b &amp; c &gt; d")
	require.NotContains(t, block, "<a>")
	// The command's newlines (there are none here) and the key's do not
	// break the line-oriented format.
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, "- tool=") {
			require.NotContains(t, line, "\t")
		}
	}
}

func TestBuildContextManifestOmitsEmptySections(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		manifestMsg("u1", message.User, 1000, message.TextContent{Text: "hello"}),
	}

	block := buildContextManifest(manifestInput{
		Messages:      msgs,
		AnchorAt:      2000,
		WorktreeKnown: true,
	})

	require.NotContains(t, block, "failures:")
	require.NotContains(t, block, "files:",
		"a verified-clean tree with no file tool calls has nothing to list")
	require.NotContains(t, block, "anchors:",
		"the manifest publishes no retrieval handles beyond the failure rows")
	require.Contains(t, block, "archived: messages=1")
}

// When git was not consulted, an absent file list would read as "nothing
// changed" — the exact confusion the coverage line exists to prevent.
func TestBuildContextManifestSaysWhenWorktreeUnchecked(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		manifestMsg("b", message.Assistant, 1000,
			message.ToolCall{ID: "c1", Name: "bash", Input: `{"command":"sed -i s/a/b/ x.go"}`},
		),
	}

	block := buildContextManifest(manifestInput{Messages: msgs, AnchorAt: 2000})

	require.Contains(t, block, "files:")
	require.Contains(t, block, "working tree not checked")
	require.Contains(t, block, "1 bash call(s) may have modified files")
}

func TestBuildContextManifestSaysWhenWorktreeClean(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		manifestMsg("b", message.Assistant, 1000,
			message.ToolCall{ID: "c1", Name: "bash", Input: `{"command":"go build ./..."}`},
		),
	}

	block := buildContextManifest(manifestInput{
		Messages:      msgs,
		AnchorAt:      2000,
		WorktreeKnown: true,
	})

	require.Contains(t, block, "working tree clean")
	require.Contains(t, block, "reported no changes")
}

// An edit made through a shell command leaves no path in any tool input, so
// the file list has to come from git or it is simply wrong.
func TestBuildContextManifestIncludesShellEditedFiles(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		manifestMsg("b", message.Assistant, 1000,
			message.ToolCall{ID: "c1", Name: "bash", Input: `{"command":"sed -i s/a/b/ x.go"}`},
		),
	}

	block := buildContextManifest(manifestInput{
		Messages:      msgs,
		AnchorAt:      2000,
		WorktreeKnown: true,
		Worktree: []manifestWorktreeFile{
			{Path: "x.go", Status: "modified"},
		},
	})

	require.Contains(t, block, "path=x.go")
	require.Contains(t, block, "src=git state=modified")
}

// A path written by a tool and also dirty is listed once, marked as git's
// because git is the authority on the resulting state.
func TestBuildContextManifestMergesToolAndWorktreeEntries(t *testing.T) {
	t.Parallel()

	block := buildContextManifest(manifestInput{
		Messages:      []message.Message{manifestMsg("e", message.Assistant, 1500)},
		AnchorAt:      2000,
		WorktreeKnown: true,
		Files:         []manifestFile{{Path: "a.go", Last: 1500}},
		Worktree:      []manifestWorktreeFile{{Path: "a.go", Status: "modified"}},
	})

	require.Equal(t, 1, strings.Count(block, "path=a.go"))
	require.Contains(t, block, "path=a.go last=")
	require.Contains(t, block, "src=git state=modified")
}

func TestBuildContextManifestReportsToolResultBytes(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		manifestMsg("b", message.Assistant, 1000,
			message.ToolCall{ID: "c1", Name: "bash", Input: `{"command":"cat big.log"}`},
		),
		manifestMsg("b-r", message.Tool, 1001, message.ToolResult{
			ToolCallID: "c1", Name: "bash", Content: strings.Repeat("x", 4096),
		}),
	}

	block := buildContextManifest(manifestInput{Messages: msgs, AnchorAt: 2000})

	require.Contains(t, block, "tool_result_bytes=4096")
}

func TestParseWorktreeStatus(t *testing.T) {
	t.Parallel()

	t.Run("modified, untracked and deleted", func(t *testing.T) {
		t.Parallel()

		out := " M a.go\x00?? b.go\x00 D c.go\x00"
		files := parseWorktreeStatus(out, 10)

		require.Len(t, files, 3)
		require.Equal(t, manifestWorktreeFile{Path: "a.go", Status: "modified"}, files[0])
		require.Equal(t, manifestWorktreeFile{Path: "b.go", Status: "untracked"}, files[1])
		require.Equal(t, manifestWorktreeFile{Path: "c.go", Status: "deleted"}, files[2])
	})

	t.Run("a rename skips the original path", func(t *testing.T) {
		t.Parallel()

		out := "R  new.go\x00old.go\x00 M next.go\x00"
		files := parseWorktreeStatus(out, 10)

		require.Len(t, files, 2, "the original path must not become an entry of its own")
		require.Equal(t, "new.go", files[0].Path)
		require.Equal(t, "renamed", files[0].Status)
		require.Equal(t, "next.go", files[1].Path)
	})

	t.Run("empty output", func(t *testing.T) {
		t.Parallel()
		require.Empty(t, parseWorktreeStatus("", 10))
	})

	t.Run("respects the limit", func(t *testing.T) {
		t.Parallel()

		out := " M a.go\x00 M b.go\x00 M c.go\x00"
		require.Len(t, parseWorktreeStatus(out, 2), 2)
	})
}

func TestBuildContextManifestBudgetDropsFilesFirst(t *testing.T) {
	t.Parallel()

	// Enough long paths to overrun the budget on their own: with no
	// structural cap, the loop is what trims them, one entry at a time.
	files := make([]manifestFile, 0, 60)
	for i := range 60 {
		files = append(files, manifestFile{
			Path: "internal/agent/very/long/path/to/some/file/number/" + strings.Repeat("x", 40) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".go",
			Last: int64(1000 + i),
		})
	}

	block := buildContextManifest(manifestInput{
		Messages:      []message.Message{manifestMsg("u", message.User, 1000)},
		AnchorAt:      5000,
		WorktreeKnown: true,
		Files:         files,
	})

	require.LessOrEqual(t, approxTokenCount(block), int64(manifestTokenBudget))
	require.Contains(t, block, "truncated:")
	require.Contains(t, block, "coverage:")
	require.Contains(t, block, "</context_manifest>")
}

func TestBuildContextManifestIncludesGeneratedAt(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{manifestMsg("u1", message.User, 1000)}
	at := time.Date(2026, 10, 24, 10, 12, 3, 0, time.UTC)

	block := buildContextManifest(manifestInput{
		Messages:    msgs,
		AnchorAt:    2000,
		GeneratedAt: at,
	})

	require.Contains(t, block, `generated="2026-10-24T10:12:03Z"`)
	require.Contains(t, block, "range=")
}

func TestManifestFailureKeyPrefersMarker(t *testing.T) {
	t.Parallel()

	require.Equal(t, "--- FAIL: TestX (0.1s)",
		manifestFailureKey("running\n--- FAIL: TestX (0.1s)\nok"))
	require.Equal(t, "some first line",
		manifestFailureKey("\n\nsome first line\nmore"))
	require.Empty(t, manifestFailureKey("   \n\n"))
}

func TestBuildContextManifestKeepsEveryFileUnderBudget(t *testing.T) {
	t.Parallel()

	// Files have no structural cap: the budget is the only thing that trims
	// them. More entries than the old twenty-file cap must all survive when
	// the block fits, because the entries it used to drop were real work.
	files := make([]manifestFile, 0, 25)
	for i := range 25 {
		files = append(files, manifestFile{
			Path: "file" + string(rune('a'+i)) + ".go",
			Last: int64(1000 + i),
		})
	}

	block := buildContextManifest(manifestInput{
		Messages:      []message.Message{manifestMsg("u", message.User, 1000)},
		AnchorAt:      5000,
		WorktreeKnown: true,
		Files:         files,
	})

	require.NotContains(t, block, "truncated:")
	require.Equal(t, 25, strings.Count(block, "- path="))
	require.LessOrEqual(t, approxTokenCount(block), int64(manifestTokenBudget))
}

func TestManifestEscape(t *testing.T) {
	t.Parallel()

	require.Equal(t, "a &lt;b&gt; &amp; c", manifestEscape("a <b> & c"))
	require.Equal(t, "one two", manifestEscape("one\ntwo"))
	require.Equal(t, "one two", manifestEscape("one\r\ntwo"))
	require.Equal(t, "one two", manifestEscape("one\rtwo"))
	require.Equal(t, "plain", manifestEscape("plain"))
}

func TestBuildContextManifestFailureFieldOrder(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		bashFailureMsg("f", 1000, "go test ./...", ""),
		bashResultMsg("f-r", 1001, "--- FAIL: TestX\n\nExit code 1", true),
	}

	block := buildContextManifest(manifestInput{Messages: msgs, AnchorAt: 2000})

	var line string
	for _, l := range strings.Split(block, "\n") {
		if strings.HasPrefix(l, "- tool=") {
			line = l
			break
		}
	}
	require.NotEmpty(t, line)
	// The declared fields keep their §4.4 order; the retrieval handle is
	// appended last so a parser that learned the old shape still works.
	require.Regexp(t, `^- tool=bash exit=1 cmd="go test \./\.\.\." key="--- FAIL: TestX" tool_call_id=call_f$`, line)
}

// Ties are common: results batched into one assistant message share both the
// timestamp and the message id. The order must still be stable across runs.
func TestBuildContextManifestFailureOrderIsDeterministic(t *testing.T) {
	t.Parallel()

	var calls []message.ContentPart
	for _, id := range []string{"c3", "c1", "c2"} {
		calls = append(calls, message.ToolCall{
			ID:    id,
			Name:  "bash",
			Input: `{"command":"cmd-` + id + `"}`,
		})
	}
	msgs := []message.Message{manifestMsg("batch", message.Assistant, 1000, calls...)}
	for _, id := range []string{"c3", "c1", "c2"} {
		msgs = append(msgs, manifestMsg("batch", message.Tool, 1000, message.ToolResult{
			ToolCallID: id, Name: "bash", Content: "boom\n\nExit code 1",
		}))
	}

	first := buildContextManifest(manifestInput{Messages: msgs, AnchorAt: 2000})
	for range 20 {
		require.Equal(t, first, buildContextManifest(manifestInput{Messages: msgs, AnchorAt: 2000}),
			"the manifest must not depend on map iteration order")
	}
}

func TestWorktreeDirtyFilesOutsideRepository(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	_, err := worktreeDirtyFiles(t.Context(), dir)
	require.Error(t, err, "git status in a non-repository must report the failure")
}

func TestWorktreeDirtyFilesReportsChanges(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("one"), 0o644))
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "add", "tracked.txt")
	runGit(t, dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--quiet", "-m", "init")

	clean, err := worktreeDirtyFiles(t.Context(), dir)
	require.NoError(t, err)
	require.Empty(t, clean, "a clean tree reports no files")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("two"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o644))

	dirty, err := worktreeDirtyFiles(t.Context(), dir)
	require.NoError(t, err)

	byPath := make(map[string]string, len(dirty))
	for _, f := range dirty {
		byPath[f.Path] = f.Status
	}
	require.Equal(t, "modified", byPath["tracked.txt"])
	require.Equal(t, "untracked", byPath["new.txt"])
}

func TestWorktreeDirtyFilesRejectsEmptyDir(t *testing.T) {
	t.Parallel()

	_, err := worktreeDirtyFiles(t.Context(), "")
	require.Error(t, err)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	require.NoError(t, cmd.Run(), "git %v", args)
}
