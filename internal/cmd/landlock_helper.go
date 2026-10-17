package cmd

import (
	"os"

	"github.com/charmbracelet/crush/internal/sandbox"
	"github.com/spf13/cobra"
)

// landlockHelperCmd is the hidden entry point for the Landlock backend. The
// backend re-executes the Crush binary with this subcommand so the
// irreversible restriction is applied in a process that immediately replaces
// itself with the target command; the Crush process itself is never
// restricted.
var landlockHelperCmd = &cobra.Command{
	Use:    "landlock-helper [command] [args...]",
	Hidden: true,
	Short:  "Internal: apply a Landlock ruleset and exec a command",
	// The helper must not print usage or parse flags: its argv is the target
	// command.
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		handled, code := sandbox.RunLandlockHelper(args)
		if handled && code != 0 {
			os.Exit(code)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(landlockHelperCmd)
}
