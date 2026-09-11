package tools

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/x/ansi"
)

// regexCache provides thread-safe caching of compiled regex patterns
type regexCache struct {
	*csync.Map[string, *regexp.Regexp]
}

// newRegexCache creates a new regex cache
func newRegexCache() *regexCache {
	return &regexCache{
		Map: csync.NewMap[string, *regexp.Regexp](),
	}
}

// get retrieves a compiled regex from cache or compiles and caches it
func (rc *regexCache) get(pattern string) (*regexp.Regexp, error) {
	re, ok := rc.Get(pattern)
	if ok && re != nil {
		return re, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	rc.Set(pattern, re)
	return re, nil
}

// ResetCache clears compiled regex caches to prevent unbounded growth across sessions.
func ResetCache() {
	searchRegexCache.Reset(map[string]*regexp.Regexp{})
	globRegexCache.Reset(map[string]*regexp.Regexp{})
}

// Global regex cache instances
var (
	searchRegexCache = newRegexCache()
	globRegexCache   = newRegexCache()
	// Pre-compiled regex for glob conversion (used frequently)
	globBraceRegex = regexp.MustCompile(`\{([^}]+)\}`)
)

type GrepParams struct {
	Pattern     string `json:"pattern" description:"The regex pattern to search for in file contents"`
	Path        string `json:"path,omitempty" description:"The directory to search in. Defaults to the current working directory."`
	Include     string `json:"include,omitempty" description:"File pattern to include in the search (e.g. \"*.js\", \"*.{ts,tsx}\")"`
	LiteralText bool   `json:"literal_text,omitempty" description:"If true, the pattern will be treated as literal text with special regex characters escaped. Default is false."`
	Type        string `json:"type,omitempty" description:"Only search files of this ripgrep file type (e.g. \"go\", \"js\", \"py\")."`
	Context     int    `json:"context,omitempty" description:"Number of lines to show before and after each match (max 10)."`
	IgnoreCase  bool   `json:"ignore_case,omitempty" description:"Search case-insensitively."`
	Multiline   bool   `json:"multiline,omitempty" description:"Allow the pattern to match across lines."`
}

// grepOptions is a single search request, shared by the ripgrep and pure-Go
// implementations.
type grepOptions struct {
	pattern    string
	path       string
	include    string
	typeFilter string
	context    int
	ignoreCase bool
	multiline  bool
}

type grepMatch struct {
	path     string
	modTime  time.Time
	lineNum  int
	charNum  int
	lineText string
}

type GrepResponseMetadata struct {
	NumberOfMatches int  `json:"number_of_matches"`
	Truncated       bool `json:"truncated"`
}

const (
	GrepToolName        = "grep"
	maxGrepContentWidth = 500
	maxGrepContext      = 10
)

//go:embed grep.md.tpl
var grepDescriptionTmpl []byte

var grepDescriptionTpl = template.Must(
	template.New("grepDescription").
		Parse(string(grepDescriptionTmpl)),
)

type grepDescriptionData struct {
	MaxResults  int
	RgAvailable bool
}

func grepDescription() string {
	return renderTemplate(grepDescriptionTpl, grepDescriptionData{
		MaxResults:  100,
		RgAvailable: getRg() != "",
	})
}

// escapeRegexPattern escapes special regex characters so they're treated as literal characters
func escapeRegexPattern(pattern string) string {
	specialChars := []string{"\\", ".", "+", "*", "?", "(", ")", "[", "]", "{", "}", "^", "$", "|"}
	escaped := pattern

	for _, char := range specialChars {
		escaped = strings.ReplaceAll(escaped, char, "\\"+char)
	}

	return escaped
}

func NewGrepTool(workingDir string, config config.ToolGrep) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		GrepToolName,
		grepDescription(),
		func(ctx context.Context, params GrepParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Pattern == "" {
				return fantasy.NewTextErrorResponse("pattern is required"), nil
			}

			pattern := params.Pattern
			if params.LiteralText {
				pattern = escapeRegexPattern(pattern)
			}

			opts := grepOptions{
				pattern:    pattern,
				path:       cmp.Or(params.Path, workingDir),
				include:    params.Include,
				typeFilter: params.Type,
				context:    min(max(params.Context, 0), maxGrepContext),
				ignoreCase: params.IgnoreCase,
				multiline:  params.Multiline,
			}

			searchCtx, cancel := context.WithTimeout(ctx, config.GetTimeout())
			defer cancel()

			matches, truncated, err := searchFiles(searchCtx, opts, 100)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("error searching files: %v", err)), nil
			}

			var (
				output     strings.Builder
				matchCount int
			)
			if len(matches) == 0 {
				output.WriteString("No files found")
			} else {
				for _, match := range matches {
					if match.charNum > 0 {
						matchCount++
					}
				}
				fmt.Fprintf(&output, "Found %d matches\n", matchCount)

				currentFile := ""
				for _, match := range matches {
					if currentFile != match.path {
						if currentFile != "" {
							output.WriteString("\n")
						}
						currentFile = match.path
						fmt.Fprintf(&output, "%s:\n", filepath.ToSlash(match.path))
					}
					if match.lineNum > 0 {
						lineText := match.lineText
						if ansi.StringWidth(lineText) > maxGrepContentWidth {
							lineText = ansi.Truncate(lineText, maxGrepContentWidth, "...")
						}
						if match.charNum > 0 {
							fmt.Fprintf(&output, "  Line %d, Char %d: %s\n", match.lineNum, match.charNum, lineText)
						} else {
							fmt.Fprintf(&output, "  Line %d: %s\n", match.lineNum, lineText)
						}
					} else {
						fmt.Fprintf(&output, "  %s\n", match.path)
					}
				}

				if truncated {
					output.WriteString("\n(Results are truncated. Consider using a more specific path or pattern.)")
				}
			}

			return fantasy.WithResponseMetadata(
				fantasy.NewTextResponse(output.String()),
				GrepResponseMetadata{
					NumberOfMatches: matchCount,
					Truncated:       truncated,
				},
			), nil
		},
	)
}

func searchFiles(ctx context.Context, opts grepOptions, limit int) ([]grepMatch, bool, error) {
	matches, err := searchWithRipgrep(ctx, opts)
	if err != nil {
		// type and multiline exist only in ripgrep, so refuse rather
		// than silently drop them on the fallback path.
		if opts.typeFilter != "" || opts.multiline {
			return nil, false, err
		}
		matches, err = searchFilesWithRegex(opts)
		if err != nil {
			return nil, false, err
		}
	}

	// Use a stable sort so that the multiple matches a single file can
	// contribute (all sharing the same modTime) keep their original
	// line order and stay grouped together in the rendered output.
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].modTime.After(matches[j].modTime)
	})

	truncated := len(matches) > limit
	if truncated {
		matches = matches[:limit]
	}

	return matches, truncated, nil
}

func searchWithRipgrep(ctx context.Context, opts grepOptions) ([]grepMatch, error) {
	cmd := getRgSearchCmd(ctx, opts)
	if cmd == nil {
		return nil, fmt.Errorf("ripgrep not found in $PATH")
	}

	// Only add ignore files if they exist
	for _, ignoreFile := range []string{".gitignore", ".crushignore"} {
		ignorePath := filepath.Join(opts.path, ignoreFile)
		if _, err := os.Stat(ignorePath); err == nil {
			cmd.Args = append(cmd.Args, "--ignore-file", ignorePath)
		}
	}

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return []grepMatch{}, nil
		}
		return nil, err
	}
	return parseRipgrepOutput(output), nil
}

// parseRipgrepOutput converts ripgrep's --json stream into grep matches.
// Context events (from -C) are kept as entries with a zero charNum.
func parseRipgrepOutput(output []byte) []grepMatch {
	var matches []grepMatch
	modTimes := make(map[string]time.Time)
	for line := range bytes.SplitSeq(bytes.TrimSpace(output), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var event ripgrepMatch
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		if event.Type != "match" && event.Type != "context" {
			continue
		}

		path := event.Data.Path.Text
		modTime, ok := modTimes[path]
		if !ok {
			fi, err := os.Stat(path)
			if err != nil {
				continue // Skip files we can't access
			}
			modTime = fi.ModTime()
			modTimes[path] = modTime
		}

		match := grepMatch{
			path:     path,
			modTime:  modTime,
			lineNum:  event.Data.LineNumber,
			lineText: strings.TrimSpace(event.Data.Lines.Text),
		}
		if event.Type == "match" && len(event.Data.Submatches) > 0 {
			match.charNum = event.Data.Submatches[0].Start + 1 // ensure 1-based
		}
		matches = append(matches, match)
	}
	return matches
}

type ripgrepMatch struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text string `json:"text"`
		} `json:"lines"`
		LineNumber int `json:"line_number"`
		Submatches []struct {
			Start int `json:"start"`
		} `json:"submatches"`
	} `json:"data"`
}

func searchFilesWithRegex(opts grepOptions) ([]grepMatch, error) {
	matches := []grepMatch{}

	pattern := opts.pattern
	if opts.ignoreCase {
		pattern = "(?i)" + pattern
	}
	// Use cached regex compilation
	regex, err := searchRegexCache.get(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %w", err)
	}

	var includePattern *regexp.Regexp
	if opts.include != "" {
		regexPattern := globToRegex(opts.include)
		includePattern, err = globRegexCache.get(regexPattern)
		if err != nil {
			return nil, fmt.Errorf("invalid include pattern: %w", err)
		}
	}

	// Create walker with gitignore and crushignore support
	walker := fsext.NewFastGlobWalker(opts.path)

	err = filepath.Walk(opts.path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors
		}

		if info.IsDir() {
			// Check if directory should be skipped
			if walker.ShouldSkip(path) {
				return filepath.SkipDir
			}
			return nil // Continue into directory
		}

		// Use walker's shouldSkip method for files
		if walker.ShouldSkip(path) {
			return nil
		}

		// Skip hidden files (starting with a dot) to match ripgrep's default behavior
		base := filepath.Base(path)
		if base != "." && strings.HasPrefix(base, ".") {
			return nil
		}

		if includePattern != nil && !includePattern.MatchString(path) {
			return nil
		}

		lineMatches, err := fileMatches(path, regex, opts.context)
		if err != nil {
			return nil // Skip files we can't read
		}

		for _, lm := range lineMatches {
			matches = append(matches, grepMatch{
				path:     path,
				modTime:  info.ModTime(),
				lineNum:  lm.lineNum,
				charNum:  lm.charNum,
				lineText: lm.lineText,
			})
			if len(matches) >= 200 {
				return filepath.SkipAll
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return matches, nil
}

// lineMatch is a single line in a file search result: its 1-based line
// number, the 1-based column of the first match on that line (zero for
// context lines), and the line text (with the trailing newline stripped).
type lineMatch struct {
	lineNum  int
	charNum  int
	lineText string
}

// fileMatches returns every line in filePath that matches pattern, plus up
// to context lines on either side of each match. Like ripgrep, it reports
// one entry per matching line (using the first match on the line for the
// column) and merges overlapping context windows. Match lines carry a
// column; context lines leave charNum at zero.
func fileMatches(filePath string, pattern *regexp.Regexp, context int) ([]lineMatch, error) {
	if pattern == nil {
		return nil, nil
	}
	// Only search text files.
	if !isTextFile(filePath) {
		return nil, nil
	}

	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var (
		matches  []lineMatch
		before   []lineMatch // Last `context` lines, oldest first.
		after    int         // Context lines still owed after a match.
		lastEmit int         // Last line emitted, to merge overlapping windows.
		lineNum  int
	)
	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadString('\n')
		lineNum++
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")

		if loc := pattern.FindStringIndex(line); loc != nil {
			// Emit the before-context not already covered by a
			// previous match's after-context.
			for _, prev := range before {
				if prev.lineNum > lastEmit {
					matches = append(matches, prev)
					lastEmit = prev.lineNum
				}
			}
			matches = append(matches, lineMatch{
				lineNum:  lineNum,
				charNum:  loc[0] + 1,
				lineText: line,
			})
			lastEmit = lineNum
			after = context
		} else if after > 0 {
			matches = append(matches, lineMatch{
				lineNum:  lineNum,
				lineText: line,
			})
			lastEmit = lineNum
			after--
		}

		if context > 0 {
			before = append(before, lineMatch{
				lineNum:  lineNum,
				lineText: line,
			})
			if len(before) > context {
				before = before[len(before)-context:]
			}
		}

		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}

	return matches, nil
}

// isTextFile checks if a file is a text file by examining its MIME type.
func isTextFile(filePath string) bool {
	file, err := os.Open(filePath)
	if err != nil {
		return false
	}
	defer file.Close()

	// Read first 512 bytes for MIME type detection.
	buffer := make([]byte, 512)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return false
	}

	// Detect content type.
	contentType := http.DetectContentType(buffer[:n])

	// Check if it's a text MIME type.
	return strings.HasPrefix(contentType, "text/") ||
		contentType == "application/json" ||
		contentType == "application/xml" ||
		contentType == "application/javascript" ||
		contentType == "application/x-sh"
}

func globToRegex(glob string) string {
	regexPattern := strings.ReplaceAll(glob, ".", "\\.")
	regexPattern = strings.ReplaceAll(regexPattern, "*", ".*")
	regexPattern = strings.ReplaceAll(regexPattern, "?", ".")

	// Use pre-compiled regex instead of compiling each time
	regexPattern = globBraceRegex.ReplaceAllStringFunc(regexPattern, func(match string) string {
		inner := match[1 : len(match)-1]
		return "(" + strings.ReplaceAll(inner, ",", "|") + ")"
	})

	return regexPattern
}
