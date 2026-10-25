package tools

import (
	"bytes"
	"cmp"
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/shell"
)

type BashParams struct {
	Description         string `json:"description" description:"A brief description of what the command does, try to keep it under 30 characters or so"`
	Command             string `json:"command" description:"The command to execute"`
	WorkingDir          string `json:"working_dir,omitempty" description:"The working directory to execute the command in (defaults to current directory)"`
	RunInBackground     bool   `json:"run_in_background,omitempty" description:"Set to true (boolean) to run this command in the background. Use job_output to read the output later."`
	AutoBackgroundAfter int    `json:"auto_background_after,omitempty" description:"Seconds to wait before automatically moving the command to a background job (default: 60)"`
	SandboxPermissions  string `json:"sandbox_permissions,omitempty" description:"Request a wider sandbox mode for this one call only. Requires justification. The user is asked to approve leaving the sandbox and reads the justification there; approval never carries to another call. Accepted: read-only, workspace-write, danger-full-access"`
	Justification       string `json:"justification,omitempty" description:"A sentence explaining why this call needs a wider sandbox mode. Required whenever sandbox_permissions is set, and ignored when it is not. Shown to the user in the approval dialog: state what the call will do, why it must leave the sandbox, and any alternative considered"`
}

type BashPermissionsParams struct {
	Description         string `json:"description"`
	Command             string `json:"command"`
	WorkingDir          string `json:"working_dir"`
	RunInBackground     bool   `json:"run_in_background"`
	AutoBackgroundAfter int    `json:"auto_background_after"`
	SandboxPermissions  string `json:"sandbox_permissions,omitempty"`
	Justification       string `json:"justification,omitempty"`
}

type BashResponseMetadata struct {
	StartTime        int64  `json:"start_time"`
	EndTime          int64  `json:"end_time"`
	Output           string `json:"output"`
	Description      string `json:"description"`
	WorkingDirectory string `json:"working_directory"`
	// Command is the command that was actually executed. It can differ
	// from the tool call's original input when a PreToolUse hook rewrote
	// it, so the UI can display what really ran.
	Command    string `json:"command,omitempty"`
	Background bool   `json:"background,omitempty"`
	ShellID    string `json:"shell_id,omitempty"`
}

const (
	BashToolName = "bash"

	DefaultAutoBackgroundAfter = 60 // Commands taking longer automatically become background jobs
	MaxOutputLength            = 30000
	BashNoOutput               = "no output"
)

// isBashToolName reports whether a tool name refers to a bash tool. MCP
// tools are namespaced as "<server>_<tool>", so a suffix check is used rather
// than equality.
func IsBashToolName(name string) bool {
	return name == BashToolName || strings.HasSuffix(name, "_"+BashToolName)
}

//go:embed bash.md.tpl
var bashDescriptionTmpl []byte

var bashDescriptionTpl = template.Must(
	template.New("bashDescription").
		Parse(string(bashDescriptionTmpl)),
)

type bashDescriptionData struct {
	MaxOutputLength int
	Attribution     config.Attribution
	ModelID         string
	RgAvailable     bool
	// FdCommand is the command name for fd ("fd" or "fdfind"), or empty when
	// fd is not installed.
	FdCommand   string
	GhAvailable bool
}

func bashDescription(attribution *config.Attribution, modelID string) string {
	var out bytes.Buffer
	if err := bashDescriptionTpl.Execute(&out, bashDescriptionData{
		MaxOutputLength: MaxOutputLength,
		Attribution:     *attribution,
		ModelID:         modelID,
		RgAvailable:     getRg() != "",
		FdCommand:       getFd(),
		GhAvailable:     ghAvailable,
	}); err != nil {
		// this should never happen.
		panic("failed to execute bash description template: " + err.Error())
	}
	return out.String()
}

// NewBashTool builds the bash tool. ft may be nil, in which case the session
// gets no exec ledger entry: the command still runs, only the record of it is
// skipped.
func NewBashTool(permissions permission.Service, workingDir, spillDir string, attribution *config.Attribution, modelID string, networkCommands []string, filetracker filetracker.Service) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		BashToolName,
		withSandboxNote(bashDescription(attribution, modelID)),
		func(ctx context.Context, params BashParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Command == "" {
				return fantasy.NewTextErrorResponse("missing command"), nil
			}

			// Validate the escalation pair before anything else: one without
			// the other is malformed and must be rejected before execution.
			escalation, err := parseEscalation(params.SandboxPermissions, params.Justification)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			// Determine working directory
			execWorkingDir := cmp.Or(params.WorkingDir, workingDir)

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, fmt.Errorf("session ID is required for executing shell command")
			}
			// A validated escalation asks for a wider mode for this call
			// before the policy for the command is resolved.
			sessionPolicy := SandboxPolicyFromContext(ctx)
			escalatedPolicy, escResp, err := resolveEscalation(ctx, permissions, BashToolName, escalation, sessionPolicy, call, BashPermissionsParams(params))
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			if escResp.Content != "" || escResp.IsError {
				return escResp, nil
			}
			ctx = WithSandboxPolicy(ctx, escalatedPolicy)
			// An approved escalation is also the approval for the command
			// itself; asking again would let a second prompt reverse it.
			if escalation != nil {
				ctx = withEscalationApproval(ctx, call.ID)
			}

			// The approval question is now exactly one: does this command
			// leave the sandbox's boundary? Under a sandbox, only network
			// commands do (writes are fenced, not prompted); under
			// danger-full-access the user has delegated fully and nothing
			// prompts.
			policy := SandboxPolicyFromContext(ctx)
			if requiresApproval(policy, isNetworkCommand(params.Command, networkCommands)) {
				p, err := requestPermission(
					ctx,
					permissions,
					sandboxedRequest(ctx, permission.CreatePermissionRequest{
						SessionID:   sessionID,
						Path:        execWorkingDir,
						ToolCallID:  call.ID,
						ToolName:    BashToolName,
						Action:      "execute",
						Description: fmt.Sprintf("Execute command (network): %s", params.Command),
						Params:      BashPermissionsParams(params),
					}),
				)
				if err != nil {
					return fantasy.ToolResponse{}, err
				}
				if !p {
					return NewPermissionDeniedResponse(), nil
				}
			}

			// Record the command once every approval has been granted, so a
			// denied or malformed call does not enter the ledger as something
			// the session ran. A failed command is still recorded: the
			// resumption question is "what was run", not "what succeeded",
			// and its exit status is already in the result. Files it touched
			// cannot be derived from the command, so only the command line is
			// kept.
			if filetracker != nil {
				filetracker.RecordExec(ctx, sessionID, params.Command)
			}

			// If explicitly requested as background, start immediately with detached context
			if params.RunInBackground {
				startTime := time.Now()
				bgManager := shell.GetBackgroundShellManager()
				bgManager.Cleanup()
				// Use background context so it continues after tool returns
				bgShell, err := bgManager.StartWithPolicy(context.Background(), execWorkingDir, SandboxPolicyFromContext(ctx), params.Command, params.Description)
				if err != nil {
					return fantasy.ToolResponse{}, fmt.Errorf("error starting background shell: %w", err)
				}

				// Wait a short time to detect fast failures (blocked commands, syntax errors, etc.)
				time.Sleep(1 * time.Second)
				stdout, stderr, done, execErr := bgShell.GetOutput()

				if done {
					// Command failed or completed very quickly
					bgManager.Remove(bgShell.ID)

					interrupted := shell.IsInterrupt(execErr)
					exitCode := shell.ExitCode(execErr)
					if exitCode == 0 && !interrupted && execErr != nil {
						return fantasy.ToolResponse{}, fmt.Errorf("[Job %s] error executing command: %w", bgShell.ID, execErr)
					}

					stdout = formatOutput(ctx, stdout, stderr, execErr, spillDir)

					metadata := BashResponseMetadata{
						StartTime:        startTime.UnixMilli(),
						EndTime:          time.Now().UnixMilli(),
						Output:           stdout,
						Description:      params.Description,
						WorkingDirectory: bgShell.WorkingDir,
						Command:          params.Command,
						Background:       params.RunInBackground,
					}
					if stdout == "" {
						return fantasy.WithResponseMetadata(fantasy.NewTextResponse(BashNoOutput), metadata), nil
					}
					stdout += fmt.Sprintf("\n\n<cwd>%s</cwd>", normalizeWorkingDir(bgShell.WorkingDir))
					return fantasy.WithResponseMetadata(fantasy.NewTextResponse(stdout), metadata), nil
				}

				// Still running after fast-failure check - return as background job
				metadata := BashResponseMetadata{
					StartTime:        startTime.UnixMilli(),
					EndTime:          time.Now().UnixMilli(),
					Description:      params.Description,
					WorkingDirectory: bgShell.WorkingDir,
					Command:          params.Command,
					Background:       true,
					ShellID:          bgShell.ID,
				}
				response := fmt.Sprintf("Background shell started with ID: %s\n\nUse job_output tool to view output or job_kill to terminate.", bgShell.ID)
				return fantasy.WithResponseMetadata(fantasy.NewTextResponse(response), metadata), nil
			}

			// Start synchronous execution with auto-background support
			startTime := time.Now()

			// Start with detached context so it can survive if moved to background
			bgManager := shell.GetBackgroundShellManager()
			bgManager.Cleanup()
			bgShell, err := bgManager.StartWithPolicy(context.Background(), execWorkingDir, SandboxPolicyFromContext(ctx), params.Command, params.Description)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("error starting shell: %w", err)
			}

			// Wait for either completion, auto-background threshold, or context cancellation
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()

			autoBackgroundAfter := cmp.Or(params.AutoBackgroundAfter, DefaultAutoBackgroundAfter)
			autoBackgroundThreshold := time.Duration(autoBackgroundAfter) * time.Second
			timeout := time.After(autoBackgroundThreshold)

			var stdout, stderr string
			var done bool
			var execErr error

		waitLoop:
			for {
				select {
				case <-ticker.C:
					stdout, stderr, done, execErr = bgShell.GetOutput()
					if done {
						break waitLoop
					}
				case <-timeout:
					stdout, stderr, done, execErr = bgShell.GetOutput()
					break waitLoop
				case <-ctx.Done():
					// Incoming context was cancelled before we moved to background
					// Kill the shell and return error
					bgManager.Kill(bgShell.ID)
					return fantasy.ToolResponse{}, ctx.Err()
				}
			}

			if done {
				// Command completed within threshold - return synchronously
				// Remove from background manager since we're returning directly
				// Don't call Kill() as it cancels the context and corrupts the exit code
				bgManager.Remove(bgShell.ID)

				interrupted := shell.IsInterrupt(execErr)
				exitCode := shell.ExitCode(execErr)
				if exitCode == 0 && !interrupted && execErr != nil {
					return fantasy.ToolResponse{}, fmt.Errorf("[Job %s] error executing command: %w", bgShell.ID, execErr)
				}

				stdout = formatOutput(ctx, stdout, stderr, execErr, spillDir)

				metadata := BashResponseMetadata{
					StartTime:        startTime.UnixMilli(),
					EndTime:          time.Now().UnixMilli(),
					Output:           stdout,
					Description:      params.Description,
					WorkingDirectory: bgShell.WorkingDir,
					Command:          params.Command,
					Background:       params.RunInBackground,
				}
				if stdout == "" {
					return fantasy.WithResponseMetadata(fantasy.NewTextResponse(BashNoOutput), metadata), nil
				}
				stdout += fmt.Sprintf("\n\n<cwd>%s</cwd>", normalizeWorkingDir(bgShell.WorkingDir))
				return fantasy.WithResponseMetadata(fantasy.NewTextResponse(stdout), metadata), nil
			}

			// Still running - keep as background job
			metadata := BashResponseMetadata{
				StartTime:        startTime.UnixMilli(),
				EndTime:          time.Now().UnixMilli(),
				Description:      params.Description,
				WorkingDirectory: bgShell.WorkingDir,
				Command:          params.Command,
				Background:       true,
				ShellID:          bgShell.ID,
			}
			response := fmt.Sprintf("Command is taking longer than expected and has been moved to background.\n\nBackground shell ID: %s\n\nUse job_output tool to view output or job_kill to terminate.", bgShell.ID)
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(response), metadata), nil
		},
	)
}

// formatOutput formats the output of a completed command with error handling
func formatOutput(ctx context.Context, stdout, stderr string, execErr error, spillDir string) string {
	interrupted := shell.IsInterrupt(execErr)
	exitCode := shell.ExitCode(execErr)

	// Spill files are written by Crush, not the command, so they must land
	// somewhere the policy allows: the policy's private temp directory when
	// one is attached, the data directory otherwise.
	if dir := spillDirForContext(ctx); dir != "" {
		spillDir = dir
	}

	stdout = truncateOutput(stdout, spillDir)
	stderr = truncateOutput(stderr, spillDir)

	errorMessage := stderr
	if errorMessage == "" && execErr != nil {
		errorMessage = execErr.Error()
	}

	if interrupted {
		if errorMessage != "" {
			errorMessage += "\n"
		}
		errorMessage += "Command was aborted before completion"
	} else if exitCode != 0 {
		if errorMessage != "" {
			errorMessage += "\n"
		}
		errorMessage += fmt.Sprintf("Exit code %d", exitCode)
	}

	hasBothOutputs := stdout != "" && stderr != ""

	if hasBothOutputs {
		stdout += "\n"
	}

	if errorMessage != "" {
		stdout += "\n" + errorMessage
	}

	return stdout
}

// TruncateOutput keeps the head and tail of oversized tool output. The
// dropped middle is written in full to a file under spillDir so the agent
// can read it back rather than losing it for good.
func TruncateOutput(content, spillDir string) string {
	if ansi.StringWidth(content) <= MaxOutputLength {
		return content
	}

	halfLength := MaxOutputLength / 2
	start := ansi.Truncate(content, halfLength, "")
	end := ansi.TruncateLeft(content, ansi.StringWidth(content)-halfLength, "")

	truncatedLinesCount := max(strings.Count(content, "\n")-strings.Count(start, "\n")-strings.Count(end, "\n"), 0)
	marker := fmt.Sprintf("... [%d lines truncated] ...", truncatedLinesCount)
	if path, err := shell.SpillOutput(ansi.Strip(content), spillDir); err == nil {
		marker = fmt.Sprintf("... [%d lines truncated, full output: %s] ...", truncatedLinesCount, path)
	} else {
		slog.Debug("Could not spill tool output", "dir", spillDir, "error", err)
	}
	return fmt.Sprintf("%s\n\n%s\n\n%s", start, marker, end)
}

func truncateOutput(content, spillDir string) string {
	return TruncateOutput(content, spillDir)
}

func normalizeWorkingDir(path string) string {
	if runtime.GOOS == "windows" {
		path = strings.ReplaceAll(path, fsext.WindowsWorkingDirDrive(), "")
	}
	return filepath.ToSlash(path)
}
