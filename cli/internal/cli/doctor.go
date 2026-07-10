package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/epistola-app/portablevps/internal/adapters"
	"github.com/epistola-app/portablevps/internal/config"
	"github.com/epistola-app/portablevps/internal/core"
	"github.com/epistola-app/portablevps/internal/output"
)

func newDoctorCmd(g *globalOptions) *cobra.Command {
	var server string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check the operator environment and consumer repository",
		Long: "Runs read-only checks (tooling, repo layout, flake evaluation, " +
			"operator keys, and — with --server — per-server provider/backup/secrets) " +
			"and reports OK/WARN/FAIL. Exits non-zero if anything fails.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repoRoot, err := config.ResolveRepoRoot(g.project)
			if err != nil {
				return err
			}
			if server == "" {
				server = os.Getenv("SERVER")
			}

			env := core.Env{
				RepoRoot:   repoRoot,
				Runner:     adapters.ExecRunner{},
				HasCommand: adapters.HasCommand,
				Getenv:     os.Getenv,
			}
			checks := core.RunDoctor(env, server)

			if g.json {
				if err := output.RenderDoctorJSON(cmd.OutOrStdout(), checks); err != nil {
					return err
				}
			} else {
				output.RenderDoctorHuman(cmd.OutOrStdout(), checks)
			}

			if core.HasFailures(checks) {
				return ExitError{Code: 1, Message: "doctor found problems that will block operations"}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&server, "server", "",
		"run per-server checks against this server (default: $SERVER)")
	return cmd
}
