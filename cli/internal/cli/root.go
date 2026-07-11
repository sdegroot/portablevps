// Package cli is the thin command layer: it parses arguments, wires the real
// adapters into core, and renders output. It contains no business logic.
package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// globalOptions are flags available on every command.
type globalOptions struct {
	project string
	json    bool

	// interactivity / confirmation
	nonInteractive  bool
	interactiveFlag bool
	yes             bool
	confirm         string

	// shared across nouns that act on a server
	serverFlag string
}

func newRootCmd() *cobra.Command {
	opts := &globalOptions{}
	root := &cobra.Command{
		Use:   "portablevps",
		Short: "Create, operate, and move portable single-instance VPS servers",
		Long: "portablevps provisions, operates, and migrates single-instance " +
			"application servers defined in a consumer repository. It is designed " +
			"to be driven both interactively and from CI.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	pf := root.PersistentFlags()
	pf.StringVar(&opts.project, "project", "",
		"consumer repository root (default: $PORTABLEVPS_PROJECT or the current directory)")
	pf.BoolVar(&opts.json, "json", false, "emit machine-readable JSON output (implies --non-interactive)")
	pf.BoolVar(&opts.nonInteractive, "non-interactive", false, "never prompt; missing input is an error (auto-on under $CI or no TTY)")
	pf.BoolVar(&opts.interactiveFlag, "interactive", false, "force interactive prompts even without a TTY")
	pf.BoolVar(&opts.yes, "yes", false, "pre-answer benign confirmations (does NOT satisfy a destructive gate)")
	pf.StringVar(&opts.confirm, "confirm", "", "confirm a destructive action non-interactively (must equal the target host/server)")

	root.AddCommand(newDoctorCmd(opts))
	root.AddCommand(newSecretCmd(opts))
	root.AddCommand(newServerCmd(opts))
	root.AddCommand(newServiceCmd(opts))
	root.AddCommand(newBackupCmd(opts))
	root.AddCommand(newNetworkCmd(opts))
	root.AddCommand(newTestCmd(opts))
	root.AddCommand(newKeyCmd(opts))
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	err := newRootCmd().Execute()
	if err == nil {
		return 0
	}
	// ExitError is handled first so a command can choose an empty (already
	// printed) message.
	var exit ExitError
	if errors.As(err, &exit) {
		if exit.Message != "" {
			fmt.Fprintln(os.Stderr, "error:", exit.Message)
		}
		return exit.Code
	}
	// Any error carrying its own exit code (confirm/secrets/... failures).
	var coder interface{ ExitCode() int }
	if errors.As(err, &coder) {
		fmt.Fprintln(os.Stderr, "error:", err)
		return coder.ExitCode()
	}
	fmt.Fprintln(os.Stderr, "error:", err)
	return 1
}
