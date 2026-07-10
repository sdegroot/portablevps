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
	root.PersistentFlags().StringVar(&opts.project, "project", "",
		"consumer repository root (default: $PORTABLEVPS_PROJECT or the current directory)")
	root.PersistentFlags().BoolVar(&opts.json, "json", false, "emit machine-readable JSON output")

	root.AddCommand(newDoctorCmd(opts))
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	if err := newRootCmd().Execute(); err != nil {
		var exit ExitError
		if errors.As(err, &exit) {
			if exit.Message != "" {
				fmt.Fprintln(os.Stderr, "error:", exit.Message)
			}
			return exit.Code
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}
