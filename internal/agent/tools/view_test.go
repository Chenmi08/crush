package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/sandbox"
	"github.com/stretchr/testify/require"
)

func TestReadTextFileBoundaryCases(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "sample.txt")

	var allLines []string
	for i := range 5 {
		allLines = append(allLines, fmt.Sprintf("line %d", i+1))
	}
	require.NoError(t, os.WriteFile(filePath, []byte(strings.Join(allLines, "\n")), 0o644))

	tests := []struct {
		name        string
		offset      int
		limit       int
		wantContent string
		wantHasMore bool
	}{
		{
			name:        "exactly limit lines remaining",
			offset:      0,
			limit:       5,
			wantContent: "line 1\nline 2\nline 3\nline 4\nline 5",
			wantHasMore: false,
		},
		{
			name:        "limit plus one line remaining",
			offset:      0,
			limit:       4,
			wantContent: "line 1\nline 2\nline 3\nline 4",
			wantHasMore: true,
		},
		{
			name:        "offset at last line",
			offset:      4,
			limit:       3,
			wantContent: "line 5",
			wantHasMore: false,
		},
		{
			name:        "offset beyond eof",
			offset:      10,
			limit:       3,
			wantContent: "",
			wantHasMore: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotContent, gotHasMore, err := readTextFile(filePath, tt.offset, tt.limit, 0)
			require.NoError(t, err)
			require.Equal(t, tt.wantContent, gotContent)
			require.Equal(t, tt.wantHasMore, gotHasMore)
		})
	}
}

func TestReadTextFileTruncatesLongLines(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "longline.txt")

	longLine := strings.Repeat("a", MaxLineLength+10)
	require.NoError(t, os.WriteFile(filePath, []byte(longLine), 0o644))

	content, hasMore, err := readTextFile(filePath, 0, 1, 0)
	require.NoError(t, err)
	require.False(t, hasMore)
	require.Equal(t, strings.Repeat("a", MaxLineLength)+"...", content)
}

func TestReadTextFileLineExceeding1MB(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "huge_line.txt")

	hugeLine := strings.Repeat("A", 2*1024*1024) // 2MB — exceeds bufio.Scanner max
	require.NoError(t, os.WriteFile(filePath, []byte(hugeLine), 0o644))

	content, hasMore, err := readTextFile(filePath, 0, 1, 0)
	require.NoError(t, err)
	require.False(t, hasMore)
	require.Equal(t, strings.Repeat("A", MaxLineLength)+"...", content)
}

func TestViewToolAllowsSmallSectionsOfLargeFiles(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	filePath := filepath.Join(workingDir, "large.txt")
	lines := []string{strings.Repeat("a", MaxTextViewSize+1), "target line", "after target"}
	require.NoError(t, os.WriteFile(filePath, []byte(strings.Join(lines, "\n")), 0o644))

	tool := newViewToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	resp := runViewTool(t, tool, ctx, ViewParams{
		FilePath: filePath,
		Offset:   1,
		Limit:    1,
	})

	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "     2|target line")
	require.NotContains(t, resp.Content, "File is too large")

	var meta ViewResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Equal(t, "target line", meta.Content)
}

func TestViewToolBlocksOversizedReturnedSections(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	filePath := filepath.Join(workingDir, "large-section.txt")
	lines := make([]string, DefaultReadLimit)
	for i := range lines {
		lines[i] = strings.Repeat("a", MaxLineLength)
	}
	require.NoError(t, os.WriteFile(filePath, []byte(strings.Join(lines, "\n")), 0o644))

	tool := newViewToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	resp := runViewTool(t, tool, ctx, ViewParams{
		FilePath: filePath,
	})

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "Content section is too large")
}

func TestViewToolSendsOversizedImagesInsteadOfRejecting(t *testing.T) {
	t.Parallel()

	// A >200KB image that also exceeds the pixel ceiling must be resized and
	// returned as an image response, not rejected with a "too large" error.
	// The response carries a coordinate-mapping hint so the model can still
	// reason about the original pixels.
	workingDir := t.TempDir()
	filePath := filepath.Join(workingDir, "large.png")
	writeNoisyPNG(t, filePath, 2600, 2600)

	tool := newViewToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	ctx = context.WithValue(ctx, SupportsImagesContextKey, true)
	resp := runViewTool(t, tool, ctx, ViewParams{
		FilePath: filePath,
	})

	require.False(t, resp.IsError)
	require.Equal(t, "image", resp.Type)
	require.NotEmpty(t, resp.Data)
	require.Contains(t, resp.Content, "resized")
	require.Contains(t, resp.Content, "2600x2600")
}

func TestViewToolKeepsExistingNonVisionModelPath(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	filePath := filepath.Join(workingDir, "large.png")
	writeNoisyPNG(t, filePath, 300, 300)

	tool := newViewToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	resp := runViewTool(t, tool, ctx, ViewParams{
		FilePath: filePath,
	})

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "does not support image data")
}

func TestReadTextFileEnforcesMaxContentSize(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	filePath := filepath.Join(workingDir, "oversized.txt")
	lines := []string{
		strings.Repeat("a", MaxLineLength),
		strings.Repeat("b", MaxLineLength),
		"target line",
	}
	require.NoError(t, os.WriteFile(filePath, []byte(strings.Join(lines, "\n")), 0o644))

	content, hasMore, err := readTextFile(filePath, 0, len(lines), MaxLineLength)
	require.ErrorAs(t, err, &contentTooLargeError{})
	require.Empty(t, content)
	require.False(t, hasMore)

	content, hasMore, err = readTextFile(filePath, 2, 1, MaxLineLength)
	require.NoError(t, err)
	require.Equal(t, "target line", content)
	require.False(t, hasMore)
}

func TestReadTextFileAllowsExactMaxContentSize(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	filePath := filepath.Join(workingDir, "exact-size.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("abcd\nefgh"), 0o644))

	content, hasMore, err := readTextFile(filePath, 0, 2, len("abcd\nefgh"))
	require.NoError(t, err)
	require.Equal(t, "abcd\nefgh", content)
	require.False(t, hasMore)
}

type mockViewPermissionService struct {
	*pubsub.Broker[permission.PermissionRequest]
}

func (m *mockViewPermissionService) Request(ctx context.Context, req permission.CreatePermissionRequest) (bool, error) {
	return true, nil
}

func (m *mockViewPermissionService) Grant(req permission.PermissionRequest) bool { return true }

// countingViewPermissionService records how many permission requests the
// tool made, so tests can assert a path prompted or did not.
type countingViewPermissionService struct {
	*pubsub.Broker[permission.PermissionRequest]
	requests atomic.Int64
}

func (m *countingViewPermissionService) Request(ctx context.Context, req permission.CreatePermissionRequest) (bool, error) {
	m.requests.Add(1)
	return true, nil
}

func (m *countingViewPermissionService) Grant(req permission.PermissionRequest) bool { return true }

func (m *countingViewPermissionService) Deny(req permission.PermissionRequest) bool { return true }

func (m *countingViewPermissionService) GrantPersistent(req permission.PermissionRequest) bool {
	return true
}

func (m *countingViewPermissionService) AutoApproveSession(sessionID string) {}

func (m *countingViewPermissionService) ActiveRequest() (permission.PermissionRequest, bool) {
	return permission.PermissionRequest{}, false
}

func (m *countingViewPermissionService) SetSkipRequests(skip bool) {}

func (m *countingViewPermissionService) SkipRequests() bool {
	return false
}

func (m *countingViewPermissionService) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[permission.PermissionNotification] {
	return make(<-chan pubsub.Event[permission.PermissionNotification])
}

func (m *mockViewPermissionService) Deny(req permission.PermissionRequest) bool { return true }

func (m *mockViewPermissionService) GrantPersistent(req permission.PermissionRequest) bool {
	return true
}

func (m *mockViewPermissionService) AutoApproveSession(sessionID string) {}

func (m *mockViewPermissionService) ActiveRequest() (permission.PermissionRequest, bool) {
	return permission.PermissionRequest{}, false
}

func (m *mockViewPermissionService) SetSkipRequests(skip bool) {}

func (m *mockViewPermissionService) SkipRequests() bool {
	return false
}

func (m *mockViewPermissionService) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[permission.PermissionNotification] {
	return make(<-chan pubsub.Event[permission.PermissionNotification])
}

type mockFileTracker struct{}

func (m mockFileTracker) RecordRead(ctx context.Context, sessionID, path string) {}

func (m mockFileTracker) LastReadTime(ctx context.Context, sessionID, path string) time.Time {
	return time.Time{}
}

func (m mockFileTracker) ListReadFiles(ctx context.Context, sessionID string) ([]string, error) {
	return nil, nil
}

func TestViewToolSkipsPermissionForSandboxSpillFiles(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()

	// The spill file lives outside the working directory but inside the
	// sandbox policy's temp directory, so reading it must not prompt.
	policy := &sandbox.Policy{
		Mode:    sandbox.ModeWorkspaceWrite,
		TempDir: filepath.Join(t.TempDir(), "sandbox-tmp"),
	}
	require.NoError(t, os.MkdirAll(policy.TempDir, 0o700))
	spillPath := filepath.Join(policy.TempDir, "spill.txt")
	require.NoError(t, os.WriteFile(spillPath, []byte("truncated output"), 0o644))

	perm := &countingViewPermissionService{
		Broker: pubsub.NewBroker[permission.PermissionRequest](),
	}
	tool := NewViewTool(nil, perm, mockFileTracker{}, nil, config.NewTestStore(&config.Config{}), workingDir)

	ctx := WithSandboxPolicy(
		context.WithValue(context.Background(), SessionIDContextKey, "test-session"),
		policy,
	)
	resp := runViewTool(t, tool, ctx, ViewParams{FilePath: spillPath})

	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "truncated output")
	require.Equal(t, int64(0), perm.requests.Load(),
		"reading the session's own spill output must not prompt")

	// The same path without a policy (or outside the temp directory) still
	// prompts.
	plainCtx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	resp = runViewTool(t, tool, plainCtx, ViewParams{FilePath: spillPath})
	require.False(t, resp.IsError)
	require.Equal(t, int64(1), perm.requests.Load(),
		"without a policy the out-of-workdir read prompts as before")
}

func TestViewToolStillPromptsForSymlinkedSpillPath(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	policy := &sandbox.Policy{
		Mode:    sandbox.ModeWorkspaceWrite,
		TempDir: filepath.Join(t.TempDir(), "sandbox-tmp"),
	}
	require.NoError(t, os.MkdirAll(policy.TempDir, 0o700))

	// A link inside the temp directory pointing outside it must not be
	// exempt: CheckRead resolves symlinks first, so the resolved target's
	// location decides.
	outsideFile := filepath.Join(t.TempDir(), "outside.txt")
	require.NoError(t, os.WriteFile(outsideFile, []byte("secret"), 0o644))
	linkPath := filepath.Join(policy.TempDir, "link.txt")
	require.NoError(t, os.Symlink(outsideFile, linkPath))

	perm := &countingViewPermissionService{
		Broker: pubsub.NewBroker[permission.PermissionRequest](),
	}
	tool := NewViewTool(nil, perm, mockFileTracker{}, nil, config.NewTestStore(&config.Config{}), workingDir)
	ctx := WithSandboxPolicy(
		context.WithValue(context.Background(), SessionIDContextKey, "test-session"),
		policy,
	)
	resp := runViewTool(t, tool, ctx, ViewParams{FilePath: linkPath})
	require.False(t, resp.IsError)
	require.Equal(t, int64(1), perm.requests.Load(),
		"a symlink inside the temp directory resolving outside it must prompt")
}

func newViewToolForTest(workingDir string) fantasy.AgentTool {
	permissions := &mockViewPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}
	return NewViewTool(nil, permissions, mockFileTracker{}, nil, config.NewTestStore(&config.Config{}), workingDir)
}

func runViewTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params ViewParams) fantasy.ToolResponse {
	t.Helper()

	input, err := json.Marshal(params)
	require.NoError(t, err)

	call := fantasy.ToolCall{
		ID:    "test-call",
		Name:  ViewToolName,
		Input: string(input),
	}

	resp, err := tool.Run(ctx, call)
	require.NoError(t, err)
	return resp
}

var _ filetracker.Service = mockFileTracker{}

func TestReadBuiltinFile(t *testing.T) {
	t.Parallel()

	t.Run("reads crush-config skill", func(t *testing.T) {
		t.Parallel()

		resp, err := readBuiltinFile(ViewParams{
			FilePath: "crush://skills/crush-config/SKILL.md",
		}, nil)
		require.NoError(t, err)
		require.NotEmpty(t, resp.Content)
		require.Contains(t, resp.Content, "Crush Configuration")
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()

		resp, err := readBuiltinFile(ViewParams{
			FilePath: "crush://skills/nonexistent/SKILL.md",
		}, nil)
		require.NoError(t, err)
		require.True(t, resp.IsError)
	})

	t.Run("metadata has skill info", func(t *testing.T) {
		t.Parallel()

		resp, err := readBuiltinFile(ViewParams{
			FilePath: "crush://skills/crush-config/SKILL.md",
		}, nil)
		require.NoError(t, err)

		var meta ViewResponseMetadata
		require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
		require.Equal(t, ViewResourceSkill, meta.ResourceType)
		require.Equal(t, "crush-config", meta.ResourceName)
		require.NotEmpty(t, meta.ResourceDescription)
	})

	t.Run("respects offset", func(t *testing.T) {
		t.Parallel()

		resp, err := readBuiltinFile(ViewParams{
			FilePath: "crush://skills/crush-config/SKILL.md",
			Offset:   5,
		}, nil)
		require.NoError(t, err)
		require.NotContains(t, resp.Content, "     1|")
	})
}
