package cmd

import (
	"fmt"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/sandbox"
	"github.com/spf13/cobra"
)

// applySandboxFlags applies the sandbox-related command-line flags to the
// store's runtime overrides, then validates the resulting policy.
//
// Turning the sandbox off is deliberately a separate, explicitly named act:
// YOLO disables prompts only, so "trust the model" and "trust the model with
// my whole disk" stay separate decisions. Disabling prints a warning to stderr
// so it is visible even before the TUI starts.
func applySandboxFlags(cmd *cobra.Command, store *config.ConfigStore) error {
	mode, _ := cmd.Flags().GetString("sandbox")
	noSandbox, _ := cmd.Flags().GetBool("no-sandbox")

	validated, err := config.ValidateSandboxModeFlag(mode)
	if err != nil {
		return err
	}
	if noSandbox && validated != "" && validated != sandbox.ModeDangerFullAccess {
		return fmt.Errorf("--no-sandbox conflicts with --sandbox %s: choose one", validated)
	}

	overrides := store.Overrides()
	overrides.SandboxMode = string(validated)
	overrides.DisableSandbox = noSandbox || validated == sandbox.ModeDangerFullAccess

	rt := store.ResolveSandboxRuntime()
	if rt.Err != nil {
		return fmt.Errorf("sandbox configuration: %w", rt.Err)
	}
	return nil
}
