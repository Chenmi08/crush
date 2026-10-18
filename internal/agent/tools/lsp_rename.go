package tools

import (
	"cmp"
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"charm.land/fantasy"

	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/lsp"
	lsputil "github.com/charmbracelet/crush/internal/lsp/util"
	"github.com/charmbracelet/crush/internal/permission"
)

type RenameParams struct {
	Symbol  string `json:"symbol" description:"The symbol name to rename"`
	NewName string `json:"new_name" description:"The new name for the symbol"`
	Path    string `json:"path,omitempty" description:"The directory to search in. Defaults to the current working directory."`
}

const RenameToolName = "lsp_rename"

//go:embed lsp_rename.md
var renameDescription string

func NewRenameTool(
	lspManager *lsp.Manager,
	permissions permission.Service,
	files history.Service,
	filetracker filetracker.Service,
) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		RenameToolName,
		withSandboxNote(renameDescription),
		func(ctx context.Context, params RenameParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Symbol == "" {
				return fantasy.NewTextErrorResponse("symbol is required"), nil
			}
			if params.NewName == "" {
				return fantasy.NewTextErrorResponse("new_name is required"), nil
			}
			workingDir := cmp.Or(params.Path, ".")
			resolved, err := resolveSymbol(ctx, lspManager, params.Symbol, workingDir)
			if err != nil {
				// A search failure (ripgrep missing, for example) must not
				// masquerade as "not found": the model would retry the same
				// call instead of fixing the environment.
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			edit, err := resolved.client.Rename(ctx, resolved.path, resolved.line, resolved.char, params.NewName)
			if err != nil {
				slog.Error("Failed to rename symbol", "error", err, "symbol", params.Symbol)
				return fantasy.NewTextErrorResponse(fmt.Sprintf("rename failed: %s", err)), nil
			}
			if edit == nil {
				return fantasy.NewTextResponse(fmt.Sprintf("No rename edits generated for symbol '%s'", params.Symbol)), nil
			}

			affectedFiles := collectAffectedFiles(edit)

			// A rename rewrites files, so it goes through the same fence as
			// write/edit/multiedit: confinement must not depend on which tool
			// the agent happened to pick. Every affected file is checked
			// before anything is written, so a refusal never leaves a
			// half-renamed tree. Approval widens the policy for this call.
			for _, path := range affectedFiles {
				var fenceErr error
				var denied *fantasy.ToolResponse
				ctx, denied, fenceErr = handleFenceDenial(ctx, permissions, RenameToolName, path, call, nil)
				if fenceErr != nil {
					return fantasy.ToolResponse{}, fenceErr
				}
				if denied != nil {
					return *denied, nil
				}
			}

			sessionID := GetSessionFromContext(ctx)
			if sessionID != "" && permissions != nil {
				granted, err := requestPermission(ctx, permissions, sandboxedRequest(ctx, permission.CreatePermissionRequest{
					SessionID:   sessionID,
					ToolCallID:  call.ID,
					ToolName:    RenameToolName,
					Action:      "write",
					Description: fmt.Sprintf("Rename '%s' to '%s'", params.Symbol, params.NewName),
				}))
				if err != nil {
					return fantasy.ToolResponse{}, fmt.Errorf("permission request failed: %w", err)
				}
				if !granted {
					return NewPermissionDeniedResponse(), nil
				}
			}

			if files != nil && sessionID != "" {
				for _, path := range affectedFiles {
					content, err := os.ReadFile(path)
					if err != nil {
						slog.Warn("Failed to read file for version tracking", "path", path, "error", err)
						continue
					}
					if _, err := files.CreateVersion(ctx, sessionID, path, string(content)); err != nil {
						slog.Warn("Failed to create file version", "path", path, "error", err)
					}
				}
			}

			encoding := resolved.client.GetOffsetEncoding()
			if err := lsputil.ApplyWorkspaceEdit(*edit, encoding); err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to apply rename edits: %s", err)), nil
			}

			if filetracker != nil && sessionID != "" {
				for _, path := range affectedFiles {
					filetracker.RecordRead(ctx, sessionID, path)
				}
			}

			notifyLSPs(ctx, lspManager, "")

			var b strings.Builder
			fmt.Fprintf(&b, "Renamed '%s' to '%s' in %d file(s):\n\n", params.Symbol, params.NewName, len(affectedFiles))
			for _, f := range affectedFiles {
				fmt.Fprintf(&b, "  %s\n", f)
			}

			text := b.String()
			if len(affectedFiles) > 0 {
				text += "\n" + getDiagnostics(affectedFiles[0], lspManager)
			}

			return fantasy.NewTextResponse(text), nil
		},
	)
}
