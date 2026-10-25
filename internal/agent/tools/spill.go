package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/crush/internal/shell"
)

// spillDirOrDefault returns the directory oversized tool output should be
// written to: the sandbox policy's private temp directory when one is
// attached, the caller's configured default otherwise.
//
// Spill files are written by Crush, not by the command or the page being
// fetched, so under a confined policy they must land somewhere the fence
// allows. The policy temp directory is the only such place shared by every
// write mode.
func spillDirOrDefault(ctx context.Context, fallback string) string {
	if dir := spillDirForContext(ctx); dir != "" {
		return dir
	}
	return fallback
}

// spillForRecall writes content to the spill directory and returns the path,
// so a caller can name it in a truncation notice and read it back through
// `recall(spill_path=...)`.
//
// An empty directory means the caller has nowhere to write; reporting that as
// an error lets the notice stay quiet instead of naming a file that does not
// exist.
func spillForRecall(content, dir string) (string, error) {
	if dir == "" {
		return "", errors.New("no spill directory configured")
	}
	return shell.SpillOutput(content, dir)
}

// spillRoots returns the spill directories a `recall(spill_path=...)` call may
// read from, most specific first. There are two because a tool is constructed
// with the data directory but a confined call may have written into the
// policy's private temp directory: both are Crush-owned spill locations, and
// accepting only one would make a file written under the other unreadable.
//
// Empty entries are dropped: with no configured spill directory there is
// nothing this tool is allowed to read, which is safer than accepting /tmp.
func spillRoots(ctx context.Context, configured string) []string {
	var roots []string
	if dir := spillDirForContext(ctx); dir != "" {
		roots = append(roots, shell.SpillDir(dir))
	}
	if configured != "" {
		roots = append(roots, shell.SpillDir(configured))
	}
	return roots
}

// resolveSpillPath validates that raw names a regular file inside one of the
// permitted spill roots and opens it for reading. The containment check and
// the open are one step on purpose: validating a path and then re-opening it
// leaves a window in which a symlink could be swapped for one that escapes
// the root.
//
// Symlinks are resolved before the containment check, so a link planted in
// the spill directory cannot point at an arbitrary file. Containment is
// compared on path segments rather than a string prefix: a sibling directory
// named `shell-output-evil` must not pass as `shell-output`.
func resolveSpillPath(raw string, roots []string) (*spillFile, error) {
	if raw == "" {
		return nil, errors.New("spill_path is empty")
	}
	if len(roots) == 0 {
		return nil, errors.New("no spill directory is configured for this session")
	}

	abs, err := filepath.Abs(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid spill_path %q: %w", raw, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("cannot read spill_path %q: %w", raw, err)
	}

	for _, root := range roots {
		if root == "" {
			continue
		}
		// The root may not exist yet; fall back to the cleaned absolute
		// path so a missing root does not widen the check.
		resolvedRoot, rootErr := filepath.EvalSymlinks(root)
		if rootErr != nil {
			resolvedRoot, rootErr = filepath.Abs(root)
			if rootErr != nil {
				continue
			}
			resolvedRoot = filepath.Clean(resolvedRoot)
		}
		if withinDir(resolved, resolvedRoot) {
			return openSpillFile(resolved)
		}
	}
	return nil, fmt.Errorf(
		"spill_path %q is outside the spill directory; only files Crush spilled are readable here",
		raw,
	)
}

// withinDir reports whether path is dir itself or lives beneath it, comparing
// whole path segments. It deliberately repeats sandbox's unexported
// equivalent rather than widening that package's API for one read-side check;
// both exist because a string prefix test would accept `shell-output-evil`.
func withinDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// spillFile is one opened spill file plus what the response needs to describe
// it. It exists so the reader can page without loading a file that may be much
// larger than one page.
type spillFile struct {
	file    *os.File
	name    string
	size    int
	modTime time.Time
}

// openSpillFile opens a validated spill path for reading. Non-regular files
// are refused: a device or FIFO would block or stream without bound.
//
// The descriptor is compared with the path that passed the containment check,
// so a path replaced between that check and the open is refused instead of
// read. The reverse swap cannot slip through either: the comparison happens
// against the path, not against a second resolution of it.
func openSpillFile(path string) (*spillFile, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open spill_path %q: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close() //nolint:errcheck
		return nil, fmt.Errorf("cannot stat spill_path %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		file.Close() //nolint:errcheck
		return nil, fmt.Errorf("spill_path %q is not a regular file", path)
	}
	if atPath, statErr := os.Stat(path); statErr != nil || !os.SameFile(info, atPath) {
		file.Close() //nolint:errcheck
		return nil, fmt.Errorf("spill_path %q changed while it was being opened", path)
	}
	return &spillFile{
		file:    file,
		name:    path,
		size:    int(info.Size()),
		modTime: info.ModTime(),
	}, nil
}

func (s *spillFile) Close() error { return s.file.Close() }

// readSpillSlice returns the requested byte range of the file without
// splitting a multi-byte rune at either end. It mirrors [recallSlice]'s
// semantics, but reads only the page it needs rather than the whole file.
func (s *spillFile) readSpillSlice(offset, limit int) (recallPage, error) {
	offset, limit = recallWindow(offset, limit)
	if offset >= s.size {
		return recallPage{Start: offset, Total: s.size}, nil
	}

	// Read a few extra bytes so the end can be moved back to a rune
	// boundary without a second read.
	readLen := min(limit+utf8MaxRuneLen, s.size-offset)
	buf := make([]byte, readLen)
	n, err := s.file.ReadAt(buf, int64(offset))
	if err != nil && !errors.Is(err, io.EOF) {
		return recallPage{}, fmt.Errorf("cannot read spill file: %w", err)
	}
	chunk := string(buf[:n])

	lead := 0
	for lead < len(chunk) && !utf8.RuneStart(chunk[lead]) {
		lead++
	}
	start := offset + lead
	if start >= s.size {
		return recallPage{Start: start, Total: s.size}, nil
	}

	// The page ends at the requested limit, pulled back to a rune boundary.
	// When the file ends inside this window, everything buffered is returned
	// and no boundary is needed.
	cut := lead + limit
	if cut > len(chunk) {
		cut = len(chunk)
	} else {
		cut = runeStartAtOrBefore(chunk, cut)
	}
	end := offset + cut
	return recallPage{
		Body:  chunk[lead:cut],
		Start: start,
		End:   end,
		Total: s.size,
		More:  end < s.size,
	}, nil
}

// utf8MaxRuneLen is the largest encoded rune length, used to over-read by
// enough to find the next rune boundary.
const utf8MaxRuneLen = 4
