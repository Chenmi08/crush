package tools

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/filepathext"
	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/charmbracelet/crush/internal/imageresize"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/skills"
)

//go:embed view.md.tpl
var viewDescriptionTmpl []byte

var viewDescriptionTpl = template.Must(
	template.New("viewDescription").
		Parse(string(viewDescriptionTmpl)),
)

type viewDescriptionData struct {
	DefaultReadLimit  int
	MaxTextViewSizeKB int
}

func viewDescription() string {
	return renderTemplate(viewDescriptionTpl, viewDescriptionData{
		DefaultReadLimit:  DefaultReadLimit,
		MaxTextViewSizeKB: MaxTextViewSize / 1024,
	})
}

type ViewParams struct {
	FilePath string `json:"file_path" description:"The path to the file to read"`
	Offset   int    `json:"offset,omitempty" description:"The line number to start reading from (0-based)"`
	Limit    int    `json:"limit,omitempty" description:"The number of lines to read (defaults to 200)"`
}

type ViewResourceType string

const (
	ViewResourceUnset ViewResourceType = ""
	ViewResourceSkill ViewResourceType = "skill"
)

type ViewResponseMetadata struct {
	FilePath            string           `json:"file_path"`
	Content             string           `json:"content"`
	ResourceType        ViewResourceType `json:"resource_type,omitempty"`
	ResourceName        string           `json:"resource_name,omitempty"`
	ResourceDescription string           `json:"resource_description,omitempty"`
}

const (
	ViewToolName     = "view"
	MaxTextViewSize  = 200 * 1024 // 200KB text content section
	DefaultReadLimit = 200
	MaxLineLength    = 2000
)

type contentTooLargeError struct {
	Size int
	Max  int
}

func (e contentTooLargeError) Error() string {
	return fmt.Sprintf("content section is too large (%d bytes). Maximum size is %d bytes", e.Size, e.Max)
}

func NewViewTool(
	lspManager *lsp.Manager,
	filetracker filetracker.Service,
	skillTracker *skills.Tracker,
	cfg *config.ConfigStore,
	workingDir string,
	skillsPaths ...string,
) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		ViewToolName,
		viewDescription(),
		func(ctx context.Context, params ViewParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.FilePath == "" {
				return fantasy.NewTextErrorResponse("file_path is required"), nil
			}

			// Handle builtin skill files (crush: prefix).
			if strings.HasPrefix(params.FilePath, skills.BuiltinPrefix) {
				resp, err := readBuiltinFile(params, skillTracker)
				return resp, err
			}

			// Handle relative paths. Reads are not confined by the sandbox in
			// any mode, so reading a file outside the working directory never
			// prompts.
			filePath := filepathext.SmartJoin(workingDir, params.FilePath)

			absFilePath, err := filepath.Abs(filePath)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("error resolving file path: %w", err)
			}
			// Skill files may be read without the size limits ordinary
			// files get, and get their metadata attached.
			isSkillFile := isInSkillsPath(absFilePath, skillsPaths)

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, fmt.Errorf("session ID is required for reading files")
			}

			// Check if file exists
			fileInfo, err := os.Stat(filePath)
			if err != nil {
				if os.IsNotExist(err) {
					// Try to offer suggestions for similarly named files
					dir := filepath.Dir(filePath)
					base := filepath.Base(filePath)

					dirEntries, dirErr := os.ReadDir(dir)
					if dirErr == nil {
						var suggestions []string
						for _, entry := range dirEntries {
							if strings.Contains(strings.ToLower(entry.Name()), strings.ToLower(base)) ||
								strings.Contains(strings.ToLower(base), strings.ToLower(entry.Name())) {
								suggestions = append(suggestions, filepath.Join(dir, entry.Name()))
								if len(suggestions) >= 3 {
									break
								}
							}
						}

						if len(suggestions) > 0 {
							return fantasy.NewTextErrorResponse(fmt.Sprintf("File not found: %s\n\nDid you mean one of these?\n%s",
								filePath, strings.Join(suggestions, "\n"))), nil
						}
					}

					return fantasy.NewTextErrorResponse(fmt.Sprintf("File not found: %s", filePath)), nil
				}
				return fantasy.ToolResponse{}, fmt.Errorf("error accessing file: %w", err)
			}

			// Check if it's a directory
			if fileInfo.IsDir() {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("Path is a directory, not a file: %s", filePath)), nil
			}

			// Set default limit if not provided (no limit for SKILL.md files)
			if params.Limit <= 0 {
				if isSkillFile {
					params.Limit = 1000000 // Effectively no limit for skill files
				} else {
					params.Limit = DefaultReadLimit
				}
			}

			isSupportedImage, mimeType := getImageMimeType(filePath)
			if isSupportedImage {
				if !GetSupportsImagesFromContext(ctx) {
					modelName := GetModelNameFromContext(ctx)
					return fantasy.NewTextErrorResponse(fmt.Sprintf("This model (%s) does not support image data.", modelName)), nil
				}

				imageData, readErr := os.ReadFile(filePath)
				if readErr != nil {
					return fantasy.ToolResponse{}, fmt.Errorf("error reading image file: %w", readErr)
				}

				// Normalize-and-resize oversized images instead of rejecting
				// them. The pure strategy also re-sniffs the media type from
				// the encoded bytes, so a mismatched filename/extension
				// cannot cause a provider 400. An image that exceeds even the
				// hard ceiling produces a clear error rather than a silent
				// failure.
				norm, normErr := imageresize.Normalize(imageData, mimeType, cfg.ImageResize())
				if normErr != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("Unable to send image: %s", normErr)), nil
				}

				resp := fantasy.NewImageResponse(norm.Data, norm.MimeType)
				// Tell the model the original and displayed dimensions so
				// pixel-coordinate instructions stay accurate.
				resp.Content = norm.AppendHint("")
				return resp, nil
			}

			// Read the file content
			maxContentSize := MaxTextViewSize
			if isSkillFile {
				maxContentSize = 0
			}
			content, hasMore, err := readTextFile(filePath, params.Offset, params.Limit, maxContentSize)
			if err != nil {
				var tooLarge contentTooLargeError
				if errors.As(err, &tooLarge) {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("Content section is too large (%d bytes). Maximum size is %d bytes",
						tooLarge.Size, tooLarge.Max)), nil
				}
				return fantasy.ToolResponse{}, fmt.Errorf("error reading file: %w", err)
			}
			if !utf8.ValidString(content) {
				return fantasy.NewTextErrorResponse("File content is not valid UTF-8"), nil
			}

			openInLSPs(ctx, lspManager, filePath)
			waitForLSPDiagnostics(ctx, lspManager, filePath, 300*time.Millisecond)
			output := "<file>\n"
			output += addLineNumbers(content, params.Offset+1)

			if hasMore {
				output += fmt.Sprintf("\n\n(File has more lines. Use 'offset' parameter to read beyond line %d)",
					params.Offset+len(strings.Split(content, "\n")))
			}
			output += "\n</file>\n"
			output += getDiagnostics(filePath, lspManager)
			filetracker.RecordRead(ctx, sessionID, filePath)

			meta := ViewResponseMetadata{
				FilePath: filePath,
				Content:  content,
			}
			if isSkillFile {
				if skill, err := skills.Parse(filePath); err == nil {
					meta.ResourceType = ViewResourceSkill
					meta.ResourceName = skill.Name
					meta.ResourceDescription = skill.Description
					skillTracker.MarkLoaded(skill.Name)
				}
			}

			return fantasy.WithResponseMetadata(
				fantasy.NewTextResponse(output),
				meta,
			), nil
		},
	)
}

func addLineNumbers(content string, startLine int) string {
	if content == "" {
		return ""
	}

	lines := strings.Split(content, "\n")

	var result []string
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")

		lineNum := i + startLine
		numStr := fmt.Sprintf("%d", lineNum)

		if len(numStr) >= 6 {
			result = append(result, fmt.Sprintf("%s|%s", numStr, line))
		} else {
			paddedNum := fmt.Sprintf("%6s", numStr)
			result = append(result, fmt.Sprintf("%s|%s", paddedNum, line))
		}
	}

	return strings.Join(result, "\n")
}

func readTextFile(filePath string, offset, limit, maxContentSize int) (string, bool, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", false, err
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	skipped := 0
	for skipped < offset {
		_, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return "", false, nil
			}
			return "", false, err
		}
		skipped++
	}

	lines := make([]string, 0, min(limit, DefaultReadLimit))
	contentSize := 0

	for len(lines) < limit {
		lineText, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", false, err
		}
		lineText = strings.TrimSuffix(lineText, "\n")
		lineText = strings.TrimSuffix(lineText, "\r")
		if len(lineText) > MaxLineLength {
			// Truncate at a rune boundary to avoid splitting
			// multi-byte characters.
			lineText = strings.ToValidUTF8(lineText[:MaxLineLength], "") + "..."
		}
		projectedSize := contentSize + len(lineText)
		if len(lines) > 0 {
			projectedSize++
		}
		if maxContentSize > 0 && projectedSize > maxContentSize {
			return "", false, contentTooLargeError{Size: projectedSize, Max: maxContentSize}
		}
		contentSize = projectedSize
		lines = append(lines, lineText)
		if err == io.EOF {
			break
		}
	}

	// Peek one more line only when we filled the limit.
	hasMore := false
	if len(lines) == limit {
		lineText, peekErr := reader.ReadString('\n')
		hasMore = len(lineText) > 0 || peekErr == nil
	}

	return strings.Join(lines, "\n"), hasMore, nil
}

func getImageMimeType(filePath string) (bool, string) {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".jpg", ".jpeg":
		return true, "image/jpeg"
	case ".png":
		return true, "image/png"
	case ".gif":
		return true, "image/gif"
	case ".webp":
		return true, "image/webp"
	case ".bmp":
		return true, "image/bmp"
	case ".tif", ".tiff":
		return true, "image/tiff"
	default:
		return false, ""
	}
}

// isInSkillsPath checks if filePath is within any of the configured skills
// directories. Skill files are read without size limits and get their
// metadata attached to the response.
//
// Note that symlinks are resolved to prevent path traversal attacks via
// symbolic links.
func isInSkillsPath(filePath string, skillsPaths []string) bool {
	if len(skillsPaths) == 0 {
		return false
	}

	absFilePath, err := filepath.Abs(filePath)
	if err != nil {
		return false
	}

	evalFilePath, err := filepath.EvalSymlinks(absFilePath)
	if err != nil {
		return false
	}

	for _, skillsPath := range skillsPaths {
		absSkillsPath, err := filepath.Abs(skillsPath)
		if err != nil {
			continue
		}

		evalSkillsPath, err := filepath.EvalSymlinks(absSkillsPath)
		if err != nil {
			continue
		}

		relPath, err := filepath.Rel(evalSkillsPath, evalFilePath)
		if err == nil && !strings.HasPrefix(relPath, "..") {
			return true
		}
	}

	return false
}

// readBuiltinFile reads a file from the embedded builtin skills filesystem.
func readBuiltinFile(params ViewParams, skillTracker *skills.Tracker) (fantasy.ToolResponse, error) {
	embeddedPath := "builtin/" + strings.TrimPrefix(params.FilePath, skills.BuiltinPrefix)
	builtinFS := skills.BuiltinFS()

	data, err := fs.ReadFile(builtinFS, embeddedPath)
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("Builtin file not found: %s", params.FilePath)), nil
	}

	content := string(data)
	if !utf8.ValidString(content) {
		return fantasy.NewTextErrorResponse("File content is not valid UTF-8"), nil
	}

	limit := params.Limit
	if limit <= 0 {
		limit = 1000000 // Effectively no limit for skill files.
	}

	lines := strings.Split(content, "\n")
	offset := min(params.Offset, len(lines))
	lines = lines[offset:]

	hasMore := len(lines) > limit
	if hasMore {
		lines = lines[:limit]
	}

	output := "<file>\n"
	output += addLineNumbers(strings.Join(lines, "\n"), offset+1)
	if hasMore {
		output += fmt.Sprintf("\n\n(File has more lines. Use 'offset' parameter to read beyond line %d)",
			offset+len(lines))
	}
	output += "\n</file>\n"

	meta := ViewResponseMetadata{
		FilePath: params.FilePath,
		Content:  strings.Join(lines, "\n"),
	}
	if skill, err := skills.ParseContent(data); err == nil {
		meta.ResourceType = ViewResourceSkill
		meta.ResourceName = skill.Name
		meta.ResourceDescription = skill.Description
		skillTracker.MarkLoaded(skill.Name)
	}

	return fantasy.WithResponseMetadata(
		fantasy.NewTextResponse(output),
		meta,
	), nil
}
