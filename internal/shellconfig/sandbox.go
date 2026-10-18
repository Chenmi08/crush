package shellconfig

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"

	"github.com/charmbracelet/crush/internal/sandbox"
)

// handleSandbox implements the `sandbox` builtin.
//
// Usage:
//
//	sandbox mode <read-only|workspace-write|danger-full-access>
//	sandbox allow-write <path> [<path> ...]
//	sandbox deny-write <path> [<path> ...]
//
// It writes permissions.sandbox.* so the sandbox is configured in crushrc
// alongside the rest of the configuration, with no second file format.
//
// Validation happens here so a config error names the offending key and the
// accepted values instead of failing later at execution time.
func handleSandbox(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	b := configBuilderFromCtx(ctx)
	if b == nil {
		return nil
	}
	if len(args) < 2 {
		return usage(stderr, "usage: sandbox mode|allow-write|deny-write ...")
	}

	perms := b.section("permissions")
	box := childMap(perms, "sandbox")

	switch args[1] {
	case "mode":
		if len(args) != 3 {
			return usage(stderr, "usage: sandbox mode <read-only|workspace-write|danger-full-access>")
		}
		mode, err := sandbox.ValidateMode(args[2])
		if err != nil {
			return usage(stderr, fmt.Sprintf("sandbox mode: %s", err))
		}
		box["mode"] = string(mode)
		slog.Info("Sandbox mode set in shell config", "mode", mode)
		return nil

	case "allow-write":
		return sandboxList(box, "allow_write", args, "sandbox allow-write <path> [<path> ...]", stderr)
	case "deny-write":
		return sandboxList(box, "deny_write", args, "sandbox deny-write <path> [<path> ...]", stderr)

	default:
		return usage(stderr, fmt.Sprintf("sandbox: unknown subcommand %q (expected mode, allow-write, or deny-write)", args[1]))
	}
}

// sandboxList appends values to a sandbox list field, deduplicating. Each
// value may be repeated across calls without widening the result.
func sandboxList(box map[string]any, key string, args []string, usageMsg string, stderr io.Writer) error {
	if len(args) < 3 {
		return usage(stderr, "usage: "+usageMsg)
	}
	existing, _ := box[key].([]any)
	for _, value := range args[2:] {
		if slices.ContainsFunc(existing, func(v any) bool {
			s, ok := v.(string)
			return ok && s == value
		}) {
			continue
		}
		existing = append(existing, value)
	}
	box[key] = existing
	slog.Info("Sandbox list updated in shell config", "key", key, "values", args[2:])
	return nil
}
