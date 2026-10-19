package tools

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/diff"
	"github.com/charmbracelet/crush/internal/filepathext"
	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/permission"
)

type MultiEditOperation struct {
	OldString  string `json:"old_string" description:"The text to replace"`
	NewString  string `json:"new_string" description:"The text to replace it with"`
	ReplaceAll bool   `json:"replace_all,omitempty" description:"Replace all occurrences of old_string (default false)."`
}

type MultiEditParams struct {
	FilePath           string               `json:"file_path" description:"The absolute path to the file to modify"`
	Edits              []MultiEditOperation `json:"edits" description:"Array of edit operations to perform sequentially on the file"`
	SandboxPermissions string               `json:"sandbox_permissions,omitempty" description:"Request a wider sandbox mode for this one call only. Requires justification. The user is asked to approve leaving the sandbox and reads the justification there; approval never carries to another call. Accepted: read-only, workspace-write, danger-full-access"`
	Justification      string               `json:"justification,omitempty" description:"A sentence explaining why this call needs a wider sandbox mode. Required whenever sandbox_permissions is set, and ignored when it is not. Shown to the user in the approval dialog: state what the call will do, why it must leave the sandbox, and any alternative considered"`
}

type MultiEditPermissionsParams struct {
	FilePath           string `json:"file_path"`
	OldContent         string `json:"old_content,omitempty"`
	NewContent         string `json:"new_content,omitempty"`
	SandboxPermissions string `json:"sandbox_permissions,omitempty"`
	Justification      string `json:"justification,omitempty"`
}

type FailedEdit struct {
	Index int                `json:"index"`
	Error string             `json:"error"`
	Edit  MultiEditOperation `json:"edit"`
}

type MultiEditResponseMetadata struct {
	Additions    int          `json:"additions"`
	Removals     int          `json:"removals"`
	OldContent   string       `json:"old_content,omitempty"`
	NewContent   string       `json:"new_content,omitempty"`
	EditsApplied int          `json:"edits_applied"`
	EditsFailed  []FailedEdit `json:"edits_failed,omitempty"`
}

const MultiEditToolName = "multiedit"

//go:embed multiedit.md
var multieditDescription string

func NewMultiEditTool(
	lspManager *lsp.Manager,
	permissions permission.Service,
	files history.Service,
	filetracker filetracker.Service,
	workingDir string,
	opts FileWriteOptions,
) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		MultiEditToolName,
		withSandboxNote(multieditDescription),
		func(ctx context.Context, params MultiEditParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.FilePath == "" {
				return fantasy.NewTextErrorResponse("file_path is required"), nil
			}

			if len(params.Edits) == 0 {
				return fantasy.NewTextErrorResponse("at least one edit operation is required"), nil
			}

			escalation, err := parseEscalation(params.SandboxPermissions, params.Justification)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			params.FilePath = filepathext.SmartJoin(workingDir, params.FilePath)

			ctx, denied, err := applyEscalation(ctx, permissions, MultiEditToolName, escalation, call, MultiEditPermissionsParams{
				FilePath: params.FilePath,
				// The joined edit strings preview what the batch will do;
				// per-edit diffs are only known once the file has been read.
				NewContent: joinMultiEditPreview(params.Edits),
			})
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			if denied != nil {
				return *denied, nil
			}

			// Validate all edits before applying any
			if err := validateEdits(params.Edits); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			// Refuse an out-of-policy write before creating any parent
			// directory. A refusal asks the user once via the
			// "Leave Sandbox?" dialog; approval widens the policy for this
			// call and the edits proceed.
			ctx, denied, err = handleFenceDenial(ctx, permissions, MultiEditToolName, params.FilePath, call, MultiEditPermissionsParams{
				FilePath:   params.FilePath,
				NewContent: joinMultiEditPreview(params.Edits),
			})
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			if denied != nil {
				return *denied, nil
			}

			var response fantasy.ToolResponse

			editCtx := editContext{
				ctx:         ctx,
				files:       files,
				filetracker: filetracker,
				workingDir:  workingDir,
				opts:        opts,
			}
			// Handle file creation case (first edit has empty old_string)
			if len(params.Edits) > 0 && params.Edits[0].OldString == "" {
				response, err = processMultiEditWithCreation(editCtx, params)
			} else {
				response, err = processMultiEditExistingFile(editCtx, params)
			}

			if err != nil {
				return response, err
			}

			if response.IsError {
				return response, nil
			}

			// Notify LSP clients about the change
			notifyLSPs(ctx, lspManager, params.FilePath)

			// Wait for LSP diagnostics and add them to the response
			text := fmt.Sprintf("<result>\n%s\n</result>\n", response.Content)
			text += getDiagnostics(params.FilePath, lspManager)
			response.Content = text
			return response, nil
		},
	)
}

func validateEdits(edits []MultiEditOperation) error {
	for i, edit := range edits {
		// Only the first edit can have empty old_string (for file creation)
		if i > 0 && edit.OldString == "" {
			return fmt.Errorf("edit %d: only the first edit can have empty old_string (for file creation)", i+1)
		}
	}
	return nil
}

// joinMultiEditPreview renders the batch's edit operations as a compact
// before/after listing. It is what the sandbox-escalation dialog shows
// before the file has been read, so the user can see what the batch does
// without approving it blind.
func joinMultiEditPreview(edits []MultiEditOperation) string {
	var b strings.Builder
	for i, edit := range edits {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "edit %d:\n- %s\n+ %s", i+1, edit.OldString, edit.NewString)
	}
	return b.String()
}

// applyEditsToContent applies edits sequentially, collecting the ones that
// failed. It also reports whether any edit only matched after whitespace
// normalization.
func applyEditsToContent(currentContent string, edits []MultiEditOperation, startIndex int) (string, []FailedEdit, bool) {
	var failedEdits []FailedEdit
	var whitespaceCorrected bool
	for i, edit := range edits {
		newContent, corrected, err := applyEditToContent(currentContent, edit)
		if err != nil {
			failedEdits = append(failedEdits, FailedEdit{
				Index: startIndex + i + 1,
				Error: err.Error(),
				Edit:  edit,
			})
			continue
		}
		whitespaceCorrected = whitespaceCorrected || corrected
		currentContent = newContent
	}
	return currentContent, failedEdits, whitespaceCorrected
}

func processMultiEditWithCreation(edit editContext, params MultiEditParams) (fantasy.ToolResponse, error) {
	// First edit creates the file
	firstEdit := params.Edits[0]
	if firstEdit.OldString != "" {
		return fantasy.NewTextErrorResponse("first edit must have empty old_string for file creation"), nil
	}

	// Check if file already exists
	if _, err := os.Stat(params.FilePath); err == nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("file already exists: %s", params.FilePath)), nil
	} else if !os.IsNotExist(err) {
		return fantasy.ToolResponse{}, fmt.Errorf("failed to access file: %w", err)
	}

	// Create parent directories
	dir := filepath.Dir(params.FilePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fantasy.ToolResponse{}, fmt.Errorf("failed to create parent directories: %w", err)
	}

	currentContent, failedEdits, whitespaceCorrected := applyEditsToContent(firstEdit.NewString, params.Edits[1:], 1)

	// Get session and message IDs
	sessionID := GetSessionFromContext(edit.ctx)
	if sessionID == "" {
		return fantasy.ToolResponse{}, fmt.Errorf("session ID is required for creating a new file")
	}

	_, additions, removals := diff.GenerateDiff("", currentContent, strings.TrimPrefix(params.FilePath, edit.workingDir))

	editsApplied := len(params.Edits) - len(failedEdits)

	// A write inside the writable roots is confined by the sandbox, so it
	// runs without an approval prompt. The fence above is the only place a
	// write can ask to leave the sandbox.

	// Write the file
	err := os.WriteFile(params.FilePath, []byte(currentContent), 0o644)
	if err != nil {
		return fantasy.ToolResponse{}, fmt.Errorf("failed to write file: %w", err)
	}

	// Update file history
	_, err = edit.files.Create(edit.ctx, sessionID, params.FilePath, "")
	if err != nil {
		return fantasy.ToolResponse{}, fmt.Errorf("error creating file history: %w", err)
	}

	_, err = edit.files.CreateVersion(edit.ctx, sessionID, params.FilePath, currentContent)
	if err != nil {
		slog.Error("Error creating file history version", "error", err)
	}

	edit.filetracker.RecordRead(edit.ctx, sessionID, params.FilePath)

	var message string
	if len(failedEdits) > 0 {
		message = fmt.Sprintf("File created with %d of %d edits: %s (%d edit(s) failed)", editsApplied, len(params.Edits), params.FilePath, len(failedEdits))
	} else {
		message = fmt.Sprintf("File created with %d edits: %s", len(params.Edits), params.FilePath)
	}
	message = withWhitespaceNote(message, whitespaceCorrected)

	return fantasy.WithResponseMetadata(
		fantasy.NewTextResponse(message),
		MultiEditResponseMetadata{
			OldContent:   "",
			NewContent:   currentContent,
			Additions:    additions,
			Removals:     removals,
			EditsApplied: editsApplied,
			EditsFailed:  failedEdits,
		},
	), nil
}

func processMultiEditExistingFile(edit editContext, params MultiEditParams) (fantasy.ToolResponse, error) {
	sessionID, oldContent, isCrlf, resp, err := loadExistingFile(edit, params.FilePath, "session ID is required for editing a file")
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	if resp.Content != "" || resp.IsError {
		return resp, nil
	}

	currentContent, failedEdits, whitespaceCorrected := applyEditsToContent(oldContent, params.Edits, 0)

	// Check if content actually changed
	if oldContent == currentContent {
		// If we have failed edits, report them
		if len(failedEdits) > 0 {
			return fantasy.WithResponseMetadata(
				fantasy.NewTextErrorResponse(fmt.Sprintf("no changes made - all %d edit(s) failed", len(failedEdits))),
				MultiEditResponseMetadata{
					EditsApplied: 0,
					EditsFailed:  failedEdits,
				},
			), nil
		}
		return fantasy.NewTextErrorResponse("no changes made - all edits resulted in identical content"), nil
	}

	// Generate the diff for the response metadata.
	_, additions, removals := diff.GenerateDiff(oldContent, currentContent, strings.TrimPrefix(params.FilePath, edit.workingDir))

	editsApplied := len(params.Edits) - len(failedEdits)

	// A write inside the writable roots is confined by the sandbox, so it
	// runs without an approval prompt. The fence above is the only place a
	// write can ask to leave the sandbox.
	writeContent := currentContent
	if isCrlf {
		writeContent, _ = fsext.ToWindowsLineEndings(writeContent)
	}

	if err := commitFileChange(edit, sessionID, params.FilePath, oldContent, writeContent); err != nil {
		return fantasy.ToolResponse{}, err
	}

	var message string
	if len(failedEdits) > 0 {
		message = fmt.Sprintf("Applied %d of %d edits to file: %s (%d edit(s) failed)", editsApplied, len(params.Edits), params.FilePath, len(failedEdits))
	} else {
		message = fmt.Sprintf("Applied %d edits to file: %s", len(params.Edits), params.FilePath)
	}
	message = withWhitespaceNote(message, whitespaceCorrected)

	return fantasy.WithResponseMetadata(
		fantasy.NewTextResponse(message),
		MultiEditResponseMetadata{
			OldContent:   oldContent,
			NewContent:   currentContent,
			Additions:    additions,
			Removals:     removals,
			EditsApplied: editsApplied,
			EditsFailed:  failedEdits,
		},
	), nil
}

// applyEditToContent applies a single edit, reporting whether it only matched
// after whitespace normalization.
func applyEditToContent(content string, edit MultiEditOperation) (string, bool, error) {
	if edit.OldString == "" && edit.NewString == "" {
		return content, false, nil
	}

	if edit.OldString == "" {
		return "", false, fmt.Errorf("old_string cannot be empty for content replacement")
	}

	return findAndReplace(content, edit.OldString, edit.NewString, edit.ReplaceAll)
}
