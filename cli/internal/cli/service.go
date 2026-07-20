package cli

import (
	"github.com/spf13/cobra"

	"github.com/epistola-app/portablevps/internal/adapters"
	"github.com/epistola-app/portablevps/internal/core"
	"github.com/epistola-app/portablevps/internal/output"
)

// newServiceCmd is the service-identity noun: operations that move a service
// (and its service-keyed backup repo) across machines.
func newServiceCmd(g *globalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Move and restore services across machines (data movement / DR)",
	}
	cmd.PersistentFlags().StringVar(&g.serverFlag, "server", "",
		"the service's server config (default: default_server in portablevps.toml)")
	cmd.AddCommand(newServiceMigrateCmd(g), newServiceRestoreCmd(g))
	return cmd
}

func newServiceMigrateCmd(g *globalOptions) *cobra.Command {
	var sourceHost, targetHost, marker string
	var hf hostFlags
	cmd := &cobra.Command{
		Use:   "migrate [server]",
		Short: "Move a service from one running host to another via its backup repo",
		Long: "Puts the target in restore mode, takes a final source backup, stops " +
			"the source, restores onto the target, switches it to normal, and " +
			"verifies. DNS repointing is a follow-up (`network dns-sync`).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server, ctx, err := serverArg(g, args)
			if err != nil {
				return err
			}
			if sourceHost == "" || targetHost == "" {
				return ExitError{Code: 64, Message: "migrate needs --source-host and --target-host"}
			}
			ssh, cleanup, err := hf.sshAdapter(g, ctx, server)
			if err != nil {
				return err
			}
			defer cleanup()

			prog := output.NewProgress(cmd.OutOrStdout(), "service.migrate", server, g.json)
			env := core.ServiceEnv{
				RepoRoot: ctx.RepoRoot,
				Host:     ssh,
				Stream:   adapters.ExecRunner{},
				Report:   func(phase, status, msg string) { prog.Phase(phase, status, msg) },
			}
			if err := core.Migrate(env, core.MigrateOpts{
				Server: server, SourceHost: sourceHost, TargetHost: targetHost, Marker: marker,
			}); err != nil {
				prog.Result("fail", err.Error(), exitCodeOf(err))
				return silentExit(err)
			}
			prog.Result("pass", server+" migrated "+sourceHost+" -> "+targetHost, 0, "target", targetHost)
			return nil
		},
	}
	cmd.Flags().StringVar(&sourceHost, "source-host", "", "the current (source) host address")
	cmd.Flags().StringVar(&targetHost, "target-host", "", "the destination (target) host address")
	cmd.Flags().StringVar(&marker, "marker", "", "optional demo marker to verify before/after")
	cmd.Flags().StringVar(&hf.sshPort, "ssh-port", "22", "admin SSH port")
	cmd.Flags().StringVar(&hf.adminKey, "admin-key", "", "admin SSH private key override (default: 1Password/per-server file/cloud-admin fallback)")
	return cmd
}

func newServiceRestoreCmd(g *globalOptions) *cobra.Command {
	var hf hostFlags
	var marker string
	cmd := &cobra.Command{
		Use:   "restore [server]",
		Short: "Restore a service onto an already-installed host from its backup (DR)",
		Long: "Puts the host in restore mode, runs restore.sh, switches to normal, " +
			"starts apps, and verifies — for disaster recovery onto a fresh box.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server, ctx, err := serverArg(g, args)
			if err != nil {
				return err
			}
			host := hf.host
			if host == "" {
				host = defaultMeshHost(server, ctx)
			}
			if host == "" {
				return ExitError{Code: 64, Message: "no --host and no mesh host could be derived; pass --host <addr>"}
			}
			ssh, cleanup, err := hf.sshAdapter(g, ctx, server)
			if err != nil {
				return err
			}
			defer cleanup()

			prog := output.NewProgress(cmd.OutOrStdout(), "service.restore", server, g.json)
			env := core.ServiceEnv{
				RepoRoot: ctx.RepoRoot,
				Host:     ssh,
				Stream:   adapters.ExecRunner{},
				Report:   func(phase, status, msg string) { prog.Phase(phase, status, msg) },
			}
			if err := core.Restore(env, core.RestoreOpts{Server: server, Host: host, Marker: marker}); err != nil {
				prog.Result("fail", err.Error(), exitCodeOf(err))
				return silentExit(err)
			}
			prog.Result("pass", host+" restored .#"+server, 0, "host", host)
			return nil
		},
	}
	hf.register(cmd)
	cmd.Flags().StringVar(&marker, "marker", "", "optional demo marker to verify after restore")
	return cmd
}
