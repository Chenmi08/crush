package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/spf13/cobra"
)

var sandboxCmd = &cobra.Command{
	Use:   "sandbox",
	Short: "Inspect and test the filesystem sandbox",
	Long: `Inspect and test the filesystem sandbox.

The sandbox confines writes to a set of writable roots. Reads are not
confined, and network access is not isolated. Use "crush sandbox status" to see
the resolved policy and the backend that enforces it, and
"crush sandbox exec" to run one command under that policy without starting a
session.`,
}

var sandboxStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Print the resolved sandbox policy and backend",
	RunE: func(cmd *cobra.Command, _ []string) error {
		cwd, err := ResolveCwd(cmd)
		if err != nil {
			return err
		}
		store, err := loadSandboxStore(cmd, cwd)
		if err != nil {
			return err
		}

		rt := store.ResolveSandboxRuntime()
		status := store.SandboxStatus(rt)

		jsonOutput, _ := cmd.Flags().GetBool("json")
		if jsonOutput {
			out := struct {
				Mode               string   `json:"mode"`
				Backend            string   `json:"backend"`
				Enforcement        string   `json:"enforcement"`
				WriteRoots         []string `json:"write_roots,omitempty"`
				DenyWrites         []string `json:"deny_writes,omitempty"`
				Protected          int      `json:"protected_paths,omitempty"`
				Missing            string   `json:"missing,omitempty"`
				Excluded           []string `json:"excluded_commands,omitempty"`
				ConfigurationError string   `json:"configuration_error,omitempty"`
			}{
				Mode:        string(rt.RequestedMode),
				Backend:     status.Backend,
				Enforcement: string(status.Enforcement),
				Missing:     status.Missing,
				Excluded:    rt.Configuration.ExcludeCommands,
			}
			if rt.Err != nil {
				out.ConfigurationError = rt.Err.Error()
			}
			if rt.Policy != nil {
				out.WriteRoots = rt.Policy.WriteRoots
				out.DenyWrites = rt.Policy.DenyWrites
				out.Protected = len(rt.Policy.Protected)
			}
			data, err := json.Marshal(out)
			if err != nil {
				return err
			}
			cmd.Println(string(data))
			return nil
		}

		cmd.Printf("mode: %s\n", rt.RequestedMode)
		if rt.Disabled {
			cmd.Println("disabled: true (explicitly turned off; commands run unconfined)")
		}
		if rt.Err != nil {
			cmd.Printf("configuration_error: %v\n", rt.Err)
		}
		cmd.Printf("backend: %s\n", status.Backend)
		cmd.Printf("enforcement: %s\n", status.Enforcement)
		if status.Missing != "" {
			cmd.Printf("missing: %s\n", status.Missing)
		}
		if rt.Policy != nil {
			cmd.Println("write_roots:")
			for _, root := range rt.Policy.WriteRoots {
				cmd.Printf("  - %s\n", root)
			}
			if len(rt.Policy.DenyWrites) > 0 {
				cmd.Println("deny_writes:")
				for _, deny := range rt.Policy.DenyWrites {
					cmd.Printf("  - %s\n", deny)
				}
			}
			cmd.Printf("protected_paths: %d\n", len(rt.Policy.Protected))
		}
		if len(rt.Configuration.ExcludeCommands) > 0 {
			cmd.Printf("excluded_commands: %s\n", strings.Join(rt.Configuration.ExcludeCommands, ", "))
		}
		cmd.Println("reads: unrestricted (the sandbox confines writes only)")
		cmd.Println("network: not isolated")
		return nil
	},
}

var sandboxExecCmd = &cobra.Command{
	Use:   "exec [--] <command> [args...]",
	Short: "Run one command under the resolved sandbox policy",
	Long: `Run one command under the resolved sandbox policy, without starting a session.

This is the way to validate a configuration on a new host: it reports the
backend and enforcement level and exits with the command's status. A confined
command that cannot be run because no backend is usable is refused, not run
unconfined.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := ResolveCwd(cmd)
		if err != nil {
			return err
		}
		store, err := loadSandboxStore(cmd, cwd)
		if err != nil {
			return err
		}
		rt := store.ResolveSandboxRuntime()
		status := store.SandboxStatus(rt)

		cmd.Printf("sandbox: mode=%s backend=%s enforcement=%s\n", rt.RequestedMode, status.Backend, status.Enforcement)
		if status.Missing != "" {
			cmd.Printf("sandbox: %s\n", status.Missing)
		}

		command := strings.Join(quoteArgs(args), " ")
		var stdout, stderr bytes.Buffer
		result, err := shell.RunAndCapture(cmd.Context(), shell.RunOptions{
			Command: command,
			Cwd:     cwd,
			Env:     os.Environ(),
			Stdout:  &stdout,
			Stderr:  &stderr,
			Policy:  rt.Policy,
		})
		if err != nil {
			return err
		}
		if result.Output != "" {
			fmt.Fprint(cmd.OutOrStdout(), result.Output)
			if !strings.HasSuffix(result.Output, "\n") {
				fmt.Fprintln(cmd.OutOrStdout())
			}
		}
		if result.ExitCode != 0 {
			return &exitCodeError{code: result.ExitCode}
		}
		return nil
	},
}

// exitCodeError carries a command's non-zero status out of a cobra RunE
// without printing an error of its own.
type exitCodeError struct{ code int }

func (e *exitCodeError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

// loadSandboxStore loads the configuration for diagnostics without prompting
// for project trust: a diagnostic command must never execute an untrusted
// project's configuration, and must not block on stdin.
func loadSandboxStore(cmd *cobra.Command, cwd string) (*config.ConfigStore, error) {
	debug, _ := cmd.Flags().GetBool("debug")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	return config.Init(cwd, dataDir, debug)
}

// quoteArgs quotes arguments so the joined command string parses back to the
// same argv through the shell interpreter.
func quoteArgs(args []string) []string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		if arg != "" && !strings.ContainsAny(arg, " \t\n\"'\\$`;&|<>()*?[]{}~#!") {
			quoted[i] = arg
			continue
		}
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
	}
	return quoted
}

func init() {
	sandboxStatusCmd.Flags().Bool("json", false, "Output as JSON")
	sandboxCmd.AddCommand(sandboxStatusCmd)
	sandboxCmd.AddCommand(sandboxExecCmd)
	rootCmd.AddCommand(sandboxCmd)
}
