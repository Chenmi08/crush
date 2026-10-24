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

	msgs := []message.Message{
		manifestMsg("a", message.Assistant, 1000,
			message.ToolCall{ID: "c1", Name: "view", Input: `{"file_path":"internal/agent/agent.go"}`},
		),
		manifestMsg("b", message.Assistant, 2000,
			message.ToolCall{ID: "c2", Name: "view", Input: `{"file_path":"internal/agent/agent.go"}`},
		),
		manifestMsg("c", message.Assistant, 3000,
			message.ToolCall{ID: "c3", Name: "edit", Input: `{"file_path":"internal/agent/agent.go"}`},
		),
		manifestMsg("d", message.Assistant, 1500,
			message.ToolCall{ID: "c4", Name: "view", Input: `{"file_path":"internal/message/message.go"}`},
		),
	}

	block := buildContextManifest(manifestInput{Messages: msgs, AnchorAt: 4000})

	require.Contains(t, block, "files:")
	require.Equal(t, 1, strings.Count(block, "path=internal/agent/agent.go"),
		"a path is listed once, not once per operation")
	require.Contains(t, block, "ops=read,edit reads=2 edits=1")
	require.Contains(t, block, "path=internal/message/message.go ops=read reads=1")

	// The most recently touched file comes first.
	agentIdx := strings.Index(block, "path=internal/agent/agent.go")
	messageIdx := strings.Index(block, "path=internal/message/message.go")
	require.Less(t, agentIdx, messageIdx)
}

func TestBuildContextManifestSkipsUndecodableToolInput(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		manifestMsg("a", message.Assistant, 1000,
			message.ToolCall{ID: "c1", Name: "view", Input: `not json`},
			message.ToolCall{ID: "c2", Name: "view", Input: `{"file_path":"ok.go"}`},
		),
	}

	block := buildContextManifest(manifestInput{Messages: msgs, AnchorAt: 2000})

	require.Contains(t, block, "path=ok.go")
	require.NotContains(t, block, "not json")
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
		if strings.HasPrefix(line, "- id=") {
			require.NotContains(t, line, "\t")
		}
	}
}

func TestBuildContextManifestAnchors(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		manifestMsg("u1", message.User, 1000, message.TextContent{Text: "first"}),
		bashFailureMsg("f", 2000, "make", ""),
		bashResultMsg("f-r", 2001, "Error: boom\n\nExit code 1", true),
		manifestMsg("u2", message.User, 3000, message.TextContent{Text: "second"}),
	}

	block := buildContextManifest(manifestInput{Messages: msgs, AnchorAt: 4000})

	require.Contains(t, block, "anchors:")
	require.Contains(t, block, "id=u1 kind=user")
	require.Contains(t, block, "id=u2 kind=user")
	require.Contains(t, block, "id=f kind=failure")
}

// getSessionMessages rewrites the previous summary's role to user so the
// resumption prompt reads naturally. Its id is still not a recall handle:
// the next summary replaces it, so it must not be offered as an anchor.
func TestBuildContextManifestSkipsSummaryRowsInAnchors(t *testing.T) {
	t.Parallel()

	prev := manifestMsg("prev-summary", message.User, 1000, message.TextContent{Text: "old summary"})
	prev.IsSummaryMessage = true
	msgs := []message.Message{
		prev,
		manifestMsg("u1", message.User, 2000, message.TextContent{Text: "next request"}),
	}

	block := buildContextManifest(manifestInput{Messages: msgs, AnchorAt: 3000})

	require.Contains(t, block, "id=u1 kind=user")
	require.NotContains(t, block, "prev-summary")
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
	require.Contains(t, block, "anchors:")
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

	require.Contains(t, block, "path=x.go ops=none")
	require.Contains(t, block, "src=git state=modified")
}

// A path touched by a file tool and also dirty is listed once, with both
// provenances and the tool's counters intact.
func TestBuildContextManifestMergesToolAndWorktreeEntries(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		manifestMsg("e", message.Assistant, 1500,
			message.ToolCall{ID: "c1", Name: "view", Input: `{"file_path":"a.go"}`},
		),
		manifestMsg("f", message.Assistant, 1600,
			message.ToolCall{ID: "c2", Name: "edit", Input: `{"file_path":"a.go"}`},
		),
	}

	block := buildContextManifest(manifestInput{
		Messages:      msgs,
		AnchorAt:      2000,
		WorktreeKnown: true,
		Worktree:      []manifestWorktreeFile{{Path: "a.go", Status: "modified"}},
	})

	require.Equal(t, 1, strings.Count(block, "path=a.go"))
	require.Contains(t, block, "ops=read,edit reads=1 edits=1")
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

	var msgs []message.Message
	for i := range 30 {
		at := int64(1000 + i)
		msgs = append(msgs, manifestMsg("m", message.Assistant, at,
			message.ToolCall{
				ID:    "c" + string(rune('a'+i)),
				Name:  "edit",
				Input: `{"file_path":"internal/agent/very/long/path/to/some/file/number/` + strings.Repeat("x", 40) + string(rune('a'+i)) + `.go"}`,
			},
		))
	}

	block := buildContextManifest(manifestInput{Messages: msgs, AnchorAt: 5000})

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

func TestBuildContextManifestReportsStructuralCap(t *testing.T) {
	t.Parallel()

	var msgs []message.Message
	for i := range manifestMaxFiles + 5 {
		msgs = append(msgs, manifestMsg("m", message.Assistant, int64(1000+i),
			message.ToolCall{
				ID:    "c" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
				Name:  "view",
				Input: `{"file_path":"file` + string(rune('a'+i%26)) + string(rune('a'+i/26)) + `.go"}`,
			},
		))
	}

	block := buildContextManifest(manifestInput{Messages: msgs, AnchorAt: 5000})

	require.Contains(t, block, "- (truncated: 5 more)",
		"dropping entries must be visible, not silent")
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

func TestManifestFileOpReadsBothPathSpellings(t *testing.T) {
	t.Parallel()

	path, op, ok := manifestFileOp("view", `{"file_path":"a.go"}`)
	require.True(t, ok)
	require.Equal(t, "a.go", path)
	require.Equal(t, manifestOpRead, op)

	path, op, ok = manifestFileOp("edit", `{"path":"b.go"}`)
	require.True(t, ok)
	require.Equal(t, "b.go", path)
	require.Equal(t, manifestOpEdit, op)

	_, _, ok = manifestFileOp("bash", `{"command":"ls"}`)
	require.False(t, ok)

	_, _, ok = manifestFileOp("view", `{}`)
	require.False(t, ok)

	_, _, ok = manifestFileOp("view", `garbage`)
	require.False(t, ok)
}

// §4.4 is normative: the fields appear in a fixed order, so a parser or a
// reader that learned the shape is not surprised by an extra field.
func TestBuildContextManifestFailureFieldOrder(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		bashFailureMsg("f", 1000, "go test ./...", ""),
		bashResultMsg("f-r", 1001, "--- FAIL: TestX\n\nExit code 1", true),
	}

	block := buildContextManifest(manifestInput{Messages: msgs, AnchorAt: 2000})

	var line string
	for _, l := range strings.Split(block, "\n") {
		if strings.HasPrefix(l, "- id=f ") {
			line = l
			break
		}
	}
	require.NotEmpty(t, line)
	require.Regexp(t, `^- id=f tool=bash exit=1 cmd="go test \./\.\.\." key="--- FAIL: TestX"$`, line)
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

// Keeping the oldest handles would drop the current request, which §4.7 calls
// the most important thing to carry across.
func TestBuildContextManifestAnchorsKeepNewest(t *testing.T) {
	t.Parallel()

	var msgs []message.Message
	for i := range manifestMaxAnchors + 5 {
		msgs = append(msgs, manifestMsg(
			"u"+string(rune('a'+i)),
			message.User,
			int64(1000+i),
			message.TextContent{Text: "request"},
		))
	}

	block := buildContextManifest(manifestInput{Messages: msgs, AnchorAt: 5000})

	newest := "u" + string(rune('a'+manifestMaxAnchors+4))
	oldest := "u" + string(rune('a'))
	require.Contains(t, block, "id="+newest, "the most recent request must be kept")
	require.NotContains(t, block, "id="+oldest+" ", "the oldest request is the one to drop")
}

// A working directory that is not a repository must not fail the summary; the
// manifest just reports that it could not check.
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
