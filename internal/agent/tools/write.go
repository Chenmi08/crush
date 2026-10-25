package tools

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/diff"
	"github.com/charmbracelet/crush/internal/filepathext"
	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/charmbracelet/crush/internal/history"

	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/permission"
)

//go:embed write.md
var writeDescription string

type WriteParams struct {
	FilePath           string `json:"file_path" description:"The path to the file to write"`
	Content            string `json:"content" description:"The content to write to the file"`
	SandboxPermissions string `json:"sandbox_permissions,omitempty" description:"Request a wider sandbox mode for this one call only. Requires justification. The user is asked to approve leaving the sandbox and reads the justification there; approval never carries to another call. Accepted: read-only, workspace-write, danger-full-access"`
	Justification      string `json:"justification,omitempty" description:"A sentence explaining why this call needs a wider sandbox mode. Required whenever sandbox_permissions is set, and ignored when it is not. Shown to the user in the approval dialog: state what the call will do, why it must leave the sandbox, and any alternative considered"`
}

type WritePermissionsParams struct {
	FilePath           string `json:"file_path"`
	OldContent         string `json:"old_content,omitempty"`
	NewContent         string `json:"new_content,omitempty"`
	SandboxPermissions string `json:"sandbox_permissions,omitempty"`
	Justification      string `json:"justification,omitempty"`
}

type WriteResponseMetadata struct {
	Diff      string `json:"diff"`
	Additions int    `json:"additions"`
	Removals  int    `json:"removals"`
}

const WriteToolName = "write"

func NewWriteTool(
	lspManager *lsp.Manager,
	permissions permission.Service,
	files history.Service,
	filetracker filetracker.Service,
	workingDir string,
	opts FileWriteOptions,
) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		WriteToolName,
		withSandboxNote(writeDescription),
		func(ctx context.Context, params WriteParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.FilePath == "" {
				return fantasy.NewTextErrorResponse("file_path is required"), nil
			}

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, fmt.Errorf("session_id is required")
			}

			escalation, err := parseEscalation(params.SandboxPermissions, params.Justification)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			filePath := filepathext.SmartJoin(workingDir, params.FilePath)

			ctx, denied, err := applyEscalation(ctx, permissions, WriteToolName, escalation, call, WritePermissionsParams{
				FilePath:   filePath,
				NewContent: params.Content,
			})
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			if denied != nil {
				return *denied, nil
			}

			// The sandbox fence runs before any filesystem mutation so a
			// refused write leaves no directory or file behind. A refusal
			// asks the user once via the "Leave Sandbox?" dialog; approval
			// widens the policy for this call and the write proceeds.
			ctx, denied, err = handleFenceDenial(ctx, permissions, WriteToolName, filePath, call, WritePermissionsParams{
				FilePath:   filePath,
				NewContent: params.Content,
			})
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			if denied != nil {
				return *denied, nil
			}

			fileInfo, statErr := os.Stat(filePath)
			if statErr == nil {
				if fileInfo.IsDir() {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("Path is a directory, not a file: %s", filePath)), nil
				}

				// Read-before-write policy: see [FileWriteOptions].
				reason, lastRead := checkReadGuard(filetracker, ctx, sessionID, filePath, fileInfo, opts)
				switch reason {
				case readGuardBaselineMissing:
					return fantasy.NewTextErrorResponse(fmt.Sprintf("you must read the file before writing it. Use the View tool first: %s", filePath)), nil
				case readGuardStale:
					return fantasy.NewTextErrorResponse(fmt.Sprintf("File %s has been modified since it was last read.\nLast modification: %s\nLast read: %s\n\nPlease read the file again before modifying it.",
						filePath, fileInfo.ModTime().Truncate(time.Second).Format(time.RFC3339), lastRead.Format(time.RFC3339))), nil
				}

				oldContent, readErr := os.ReadFile(filePath)
				if readErr == nil && string(oldContent) == params.Content {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("File %s already contains the exact content. No changes made.", filePath)), nil
				}
			} else if !os.IsNotExist(statErr) {
				return fantasy.ToolResponse{}, fmt.Errorf("error checking file: %w", statErr)
			}

			dir := filepath.Dir(filePath)
			if err = os.MkdirAll(dir, 0o755); err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("error creating directory: %w", err)
			}

			oldContent := ""
			if fileInfo != nil && !fileInfo.IsDir() {
				oldBytes, readErr := os.ReadFile(filePath)
				if readErr == nil {
					oldContent = string(oldBytes)
				}
			}

			diff, additions, removals := diff.GenerateDiff(
				oldContent,
				params.Content,
				strings.TrimPrefix(filePath, workingDir),
			)

			// A write inside the writable roots is already confined, so it
			// runs without an approval prompt. The only approval a write can
			// raise is the fence's "Leave Sandbox?" dialog above.
			err = os.WriteFile(filePath, []byte(params.Content), 0o644)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("error writing file: %w", err)
			}

			// Check if file exists in history
			file, err := files.GetByPathAndSession(ctx, filePath, sessionID)
			if err != nil {
				_, err = files.Create(ctx, sessionID, filePath, oldContent)
				if err != nil {
					// Log error but don't fail the operation
					return fantasy.ToolResponse{}, fmt.Errorf("error creating file history: %w", err)
				}
			}
			if file.Content != oldContent {
				// User manually changed the content; store an intermediate version
				_, err = files.CreateVersion(ctx, sessionID, filePath, oldContent)
				if err != nil {
					slog.Error("Error creating file history version", "error", err)
				}
			}
			// Store the new version
			_, err = files.CreateVersion(ctx, sessionID, filePath, params.Content)
			if err != nil {
				slog.Error("Error creating file history version", "error", err)
			}

			filetracker.RecordRead(ctx, sessionID, filePath)
			filetracker.RecordWrite(ctx, sessionID, filePath)

			notifyLSPs(ctx, lspManager, params.FilePath)

			result := fmt.Sprintf("File successfully written: %s", filePath)
			result = fmt.Sprintf("<result>\n%s\n</result>", result)
			result += getDiagnostics(filePath, lspManager)
			return fantasy.WithResponseMetadata(
				fantasy.NewTextResponse(result),
				WriteResponseMetadata{
					Diff:      diff,
					Additions: additions,
					Removals:  removals,
				},
			), nil
		},
	)
}
