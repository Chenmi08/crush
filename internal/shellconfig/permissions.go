package shellconfig

import (
	"context"
	"fmt"
	"io"
	"log/slog"
)

// handlePermissions implements the `permissions` builtin.
//
// Usage:
//
//	permissions allow <tool> [<tool> ...]
//	permissions deny <tool> [<tool> ...]
//	permissions dangerous <command> [<command> ...]
//
// "allow" adds tools to the allow-list (tools that skip permission prompts).
// "deny" hides tools from the agent entirely (options.disabled_tools) — the
// inverse of allow. Adding the same tool twice is a no-op.
//
// "dangerous" adds shell command patterns that always require a fresh
// permission prompt even when the session has already allowed bash, so a
// one-time "allow for session" grant can't silently approve them. Entries
// are command-name prefixes, e.g. "git push" or "git branch -d". Adding the
// same command twice is a no-op.
//
// Precedence: deny wins. If a tool appears in both allow and deny, it is
// still removed from the agent's effective tool set via disabled_tools.
func handlePermissions(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	b := configBuilderFromCtx(ctx)
	if b == nil {
		return nil
	}
	if len(args) < 2 {
		return usage(stderr, "usage: permissions allow|deny|dangerous ...")
	}

	switch args[1] {
	case "allow":
		return permissionsAllow(b, args, stderr)
	case "deny":
		return permissionsDeny(b, args, stderr)
	case "dangerous":
		return permissionsDangerous(b, args, stderr)
	default:
		return usage(stderr, fmt.Sprintf("permissions: unknown subcommand %q (expected allow, deny, or dangerous)", args[1]))
	}
}

func permissionsAllow(b *ConfigBuilder, args []string, stderr io.Writer) error {
	if len(args) < 3 {
		return usage(stderr, "usage: permissions allow <tool> [<tool> ...]")
	}
	perms := b.section("permissions")
	allowed, _ := perms["allowed_tools"].([]any)

	for _, tool := range args[2:] {
		if !containsAny(allowed, tool) {
			allowed = append(allowed, tool)
		}
	}
	perms["allowed_tools"] = allowed

	slog.Info("Permissions allowed in shell config", "tools", args[2:])
	return nil
}

// permissionsDeny hides tools from the agent by adding them to
// options.disabled_tools. It is the inverse of allow.
func permissionsDeny(b *ConfigBuilder, args []string, stderr io.Writer) error {
	if len(args) < 3 {
		return usage(stderr, "usage: permissions deny <tool> [<tool> ...]")
	}
	opts := b.section("options")
	disabled, _ := opts["disabled_tools"].([]any)

	for _, tool := range args[2:] {
		if !containsAny(disabled, tool) {
			disabled = append(disabled, tool)
		}
	}
	opts["disabled_tools"] = disabled

	slog.Info("Permissions denied in shell config", "tools", args[2:])
	return nil
}

// permissionsDangerous adds shell command patterns to the dangerous-command
// list. Commands matching an entry always require a fresh permission prompt
// even when the session has already allowed bash, so a one-time
// "allow for session" grant can't silently approve them.
func permissionsDangerous(b *ConfigBuilder, args []string, stderr io.Writer) error {
	if len(args) < 3 {
		return usage(stderr, "usage: permissions dangerous <command> [<command> ...]")
	}
	perms := b.section("permissions")
	dangerous, _ := perms["dangerous_commands"].([]any)

	for _, cmd := range args[2:] {
		if !containsAny(dangerous, cmd) {
			dangerous = append(dangerous, cmd)
		}
	}
	perms["dangerous_commands"] = dangerous

	slog.Info("Dangerous commands in shell config", "commands", args[2:])
	return nil
}

// containsAny reports whether the slice already holds the given string value.
func containsAny(s []any, v string) bool {
	for _, item := range s {
		if str, ok := item.(string); ok && str == v {
			return true
		}
	}
	return false
}
