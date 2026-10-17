package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

// nonInteractiveTrustDefault documents the conservative default applied when
// Crush cannot ask the user: repository-local configuration is not executed.
// `crush trust` records the decision for scripts and CI.
const nonInteractiveTrustDefault = "untrusted"

var trustCmd = &cobra.Command{
	Use:   "trust",
	Short: "Manage which projects may execute their local configuration",
	Long: `Manage which projects Crush may run repository-local configuration for.

A project's crushrc and crush.json are shell code. Crush does not execute them
until the project is explicitly trusted. Trust is recorded per canonical
project path, so two checkouts of the same repository are separate decisions.

Without a terminal, Crush never prompts and treats the project as ` + nonInteractiveTrustDefault + `.
Run "crush trust" in a terminal, or "crush trust --yes", to grant trust.`,
	Example: `
# Show whether the current project is trusted
crush trust

# Trust the current project without prompting (for scripts and CI)
crush trust --yes

# Withdraw trust and stop executing the project's config on the next load
crush trust --revoke

# Operate on another project
crush trust --cwd /path/to/project --revoke`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cwd, err := ResolveCwd(cmd)
		if err != nil {
			return err
		}
		yes, _ := cmd.Flags().GetBool("yes")
		revoke, _ := cmd.Flags().GetBool("revoke")

		project := config.CanonicalProjectPath(cwd)
		switch {
		case revoke:
			if err := config.RevokeProjectTrust(project); err != nil {
				return err
			}
			cmd.Printf("Revoked trust for %s\n", project)
			return nil
		case yes:
			if err := config.TrustProject(project); err != nil {
				return err
			}
			cmd.Printf("Trusted %s\n", project)
			return nil
		}

		prompt, ok := config.NewTrustPrompt(cwd)
		if !ok {
			cmd.Printf("%s has no repository-local configuration; no trust decision is needed.\n", project)
			return nil
		}
		if config.IsProjectTrusted(project) {
			cmd.Printf("%s is trusted.\n", project)
			return nil
		}
		cmd.Println(prompt.Message())
		if !term.IsTerminal(os.Stdin.Fd()) {
			cmd.Printf("No terminal available; leaving %s untrusted.\n", project)
			return nil
		}
		fmt.Fprint(cmd.OutOrStdout(), "Trust this project? [y/N] ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			if err := config.TrustProject(project); err != nil {
				return err
			}
			cmd.Printf("Trusted %s\n", project)
		default:
			cmd.Printf("Left %s untrusted.\n", project)
		}
		return nil
	},
}

var trustListCmd = &cobra.Command{
	Use:   "list",
	Short: "List trusted projects",
	RunE: func(cmd *cobra.Command, _ []string) error {
		trusted, err := config.LoadProjectTrust()
		if err != nil {
			return err
		}
		jsonOutput, _ := cmd.Flags().GetBool("json")
		if jsonOutput {
			data, err := json.Marshal(struct {
				Projects []string `json:"projects"`
			}{Projects: trusted})
			if err != nil {
				return err
			}
			cmd.Println(string(data))
			return nil
		}
		if len(trusted) == 0 {
			cmd.Println("No projects trusted.")
			return nil
		}
		for _, p := range trusted {
			cmd.Println(p)
		}
		return nil
	},
}

// promptForProjectTrust asks once whether a project's repository-local
// configuration may run, and records the answer. It is called before config
// loading on the interactive startup path. Without a terminal it applies the
// conservative default (untrusted) rather than prompting.
func promptForProjectTrust(cwd string) error {
	prompt, ok := config.NewTrustPrompt(cwd)
	if !ok {
		return nil
	}
	project := config.CanonicalProjectPath(cwd)
	if config.IsProjectTrusted(project) {
		return nil
	}

	fmt.Fprintln(os.Stderr, prompt.Message())
	if !term.IsTerminal(os.Stdin.Fd()) {
		fmt.Fprintf(os.Stderr, "No terminal available; treating %s as untrusted. Run \"crush trust\" to grant trust.\n", project)
		return nil
	}
	fmt.Fprint(os.Stderr, "Trust this project? [y/N] ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return config.TrustProject(project)
	}
	fmt.Fprintf(os.Stderr, "Leaving %s untrusted; only global configuration will apply.\n", project)
	return nil
}

func init() {
	trustCmd.Flags().Bool("yes", false, "Trust the project without prompting")
	trustCmd.Flags().Bool("revoke", false, "Withdraw trust from the project")
	trustListCmd.Flags().Bool("json", false, "Output as JSON")
	trustCmd.AddCommand(trustListCmd)
	rootCmd.AddCommand(trustCmd)
}
