package tools

import (
	"cmp"
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/filepathext"
	"github.com/charmbracelet/crush/internal/permission"
)

type DownloadParams struct {
	URL                string `json:"url" description:"The URL to download from"`
	FilePath           string `json:"file_path" description:"The local file path where the downloaded content should be saved"`
	Timeout            int    `json:"timeout,omitempty" description:"Optional timeout in seconds (max 600)"`
	SandboxPermissions string `json:"sandbox_permissions,omitempty" description:"Request a wider sandbox mode for this one call only. Requires justification. The user is asked to approve leaving the sandbox and reads the justification there; approval never carries to another call. Accepted: read-only, workspace-write, danger-full-access"`
	Justification      string `json:"justification,omitempty" description:"A sentence explaining why this call needs a wider sandbox mode. Required whenever sandbox_permissions is set, and ignored when it is not. Shown to the user in the approval dialog: state what the call will do, why it must leave the sandbox, and any alternative considered"`
}

type DownloadPermissionsParams struct {
	URL                string `json:"url"`
	FilePath           string `json:"file_path"`
	Timeout            int    `json:"timeout,omitempty"`
	SandboxPermissions string `json:"sandbox_permissions,omitempty"`
	Justification      string `json:"justification,omitempty"`
}

const DownloadToolName = "download"

//go:embed download.md.tpl
var downloadDescriptionTmpl []byte

var downloadDescriptionTpl = template.Must(
	template.New("downloadDescription").
		Parse(string(downloadDescriptionTmpl)),
)

type downloadDescriptionData struct {
	MaxDownloadTimeout int
}

func downloadDescription() string {
	return renderTemplate(downloadDescriptionTpl, downloadDescriptionData{
		MaxDownloadTimeout: 600,
	})
}

func NewDownloadTool(permissions permission.Service, workingDir string, client *http.Client) fantasy.AgentTool {
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.MaxIdleConns = 100
		transport.MaxIdleConnsPerHost = 10
		transport.IdleConnTimeout = 90 * time.Second

		client = &http.Client{
			Timeout:   5 * time.Minute, // Default 5 minute timeout for downloads
			Transport: transport,
		}
	}
	return fantasy.NewParallelAgentTool(
		DownloadToolName,
		withSandboxNote(downloadDescription()),
		func(ctx context.Context, params DownloadParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.URL == "" {
				return fantasy.NewTextErrorResponse("URL parameter is required"), nil
			}

			if params.FilePath == "" {
				return fantasy.NewTextErrorResponse("file_path parameter is required"), nil
			}

			if !strings.HasPrefix(params.URL, "http://") && !strings.HasPrefix(params.URL, "https://") {
				return fantasy.NewTextErrorResponse("URL must start with http:// or https://"), nil
			}

			escalation, err := parseEscalation(params.SandboxPermissions, params.Justification)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			filePath := filepathext.SmartJoin(workingDir, params.FilePath)

			ctx, denied, err := applyEscalation(ctx, permissions, DownloadToolName, escalation, call, DownloadPermissionsParams(params))
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			if denied != nil {
				return *denied, nil
			}

			// Refuse an out-of-policy write before the download starts, so a
			// denied call does not fetch anything or create directories. A
			// refusal asks the user once via the "Leave Sandbox?" dialog;
			// approval widens the policy for this call and the download
			// proceeds.
			ctx, denied, err = handleFenceDenial(ctx, permissions, DownloadToolName, filePath, call, DownloadPermissionsParams(params))
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			if denied != nil {
				return *denied, nil
			}

			relPath, _ := filepath.Rel(workingDir, filePath)
			relPath = filepath.ToSlash(cmp.Or(relPath, filePath))

			// The destination is inside the writable roots (the fence above
			// approved it otherwise), so the download runs without an
			// approval prompt. Network egress is not a sandbox boundary.
			// Handle timeout with context
			requestCtx := ctx
			if params.Timeout > 0 {
				maxTimeout := 600 // 10 minutes
				if params.Timeout > maxTimeout {
					params.Timeout = maxTimeout
				}
				var cancel context.CancelFunc
				requestCtx, cancel = context.WithTimeout(ctx, time.Duration(params.Timeout)*time.Second)
				defer cancel()
			}

			req, err := http.NewRequestWithContext(requestCtx, "GET", params.URL, nil)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("failed to create request: %w", err)
			}

			req.Header.Set("User-Agent", "crush/1.0")

			resp, err := client.Do(req)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("failed to download from URL: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("Request failed with status code: %d", resp.StatusCode)), nil
			}

			// Create parent directories if they don't exist
			if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("failed to create parent directories: %w", err)
			}

			// Create the output file
			outFile, err := os.Create(filePath)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("failed to create output file: %w", err)
			}
			defer outFile.Close()

			// Copy data without an explicit size limit.
			// The overall download is still constrained by the HTTP client's timeout
			// and any upstream server limits.
			bytesWritten, err := io.Copy(outFile, resp.Body)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("failed to write file: %w", err)
			}

			contentType := resp.Header.Get("Content-Type")
			responseMsg := fmt.Sprintf("Successfully downloaded %d bytes to %s", bytesWritten, relPath)
			if contentType != "" {
				responseMsg += fmt.Sprintf(" (Content-Type: %s)", contentType)
			}

			return fantasy.NewTextResponse(responseMsg), nil
		},
	)
}
