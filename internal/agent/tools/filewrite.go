package tools

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/charmbracelet/crush/internal/filetracker"
)

// FileWriteOptions controls the read-before-write policy shared by the
// write, edit, and multiedit tools.
type FileWriteOptions struct {
	// RequireRead refuses to modify a file the session has not read with
	// the view tool. It is an escape hatch rather than the default: a
	// session that never read a file has no baseline to compare against,
	// and the tools still refuse to operate on stale content.
	RequireRead bool
}

// readGuardReason reports whether the read-before-write policy lets a tool
// modify a file.
type readGuardReason int

const (
	// readGuardOK means the modification may proceed.
	readGuardOK readGuardReason = iota
	// readGuardBaselineMissing means the session has not read the file and
	// FileWriteOptions.RequireRead is set.
	readGuardBaselineMissing
	// readGuardStale means the file changed on disk after the session last
	// read it, so the modification would operate on content the session
	// never saw.
	readGuardStale
)

// checkReadGuard applies the read-before-write policy to a file that may or
// may not have been read in this session.
//
// A file with a read baseline is always re-checked for staleness. A file
// without one is only rejected when opts.RequireRead is set; otherwise the
// modification proceeds and is logged, so blind-write frequency can be
// measured before the relaxed default is trusted.
//
// The last read time is returned alongside the reason so callers can quote
// it in their refusal.
func checkReadGuard(ft filetracker.Service, ctx context.Context, sessionID, filePath string, info os.FileInfo, opts FileWriteOptions) (readGuardReason, time.Time) {
	if ft == nil {
		return readGuardOK, time.Time{}
	}

	lastRead := ft.LastReadTime(ctx, sessionID, filePath)
	if lastRead.IsZero() {
		if opts.RequireRead {
			return readGuardBaselineMissing, lastRead
		}
		slog.Debug("Modifying a file with no read baseline", "file", filePath)
		return readGuardOK, lastRead
	}

	if info.ModTime().Truncate(time.Second).After(lastRead) {
		return readGuardStale, lastRead
	}
	return readGuardOK, lastRead
}
