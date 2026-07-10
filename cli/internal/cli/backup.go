package cli

import (
	"github.com/spf13/cobra"

	"github.com/epistola-app/portablevps/internal/core"
	"github.com/epistola-app/portablevps/internal/output"
)

// newBackupCmd is the on-host backup noun.
func newBackupCmd(g *globalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Run and inspect the on-host backup",
	}
	cmd.PersistentFlags().StringVar(&g.serverFlag, "server", "",
		"target server (default: default_server in portablevps.toml)")
	cmd.AddCommand(newBackupRunCmd(g), newBackupStatusCmd(g))
	return cmd
}

func backupHostEnv(g *globalOptions, hf *hostFlags, args []string) (core.BackupEnv, string, func(), error) {
	server, ctx, err := serverArg(g, args)
	if err != nil {
		return core.BackupEnv{}, "", func() {}, err
	}
	host := hf.host
	if host == "" {
		host = defaultMeshHost(server, ctx)
	}
	if host == "" {
		return core.BackupEnv{}, "", func() {}, ExitError{Code: 64, Message: "no --host and no mesh host could be derived; pass --host <addr>"}
	}
	ssh, cleanup, err := hf.sshAdapter(g, ctx, server)
	if err != nil {
		return core.BackupEnv{}, "", func() {}, err
	}
	return core.BackupEnv{Host: ssh}, host, cleanup, nil
}

func newBackupRunCmd(g *globalOptions) *cobra.Command {
	var hf hostFlags
	cmd := &cobra.Command{
		Use:   "run [server]",
		Short: "Trigger a backup on the server now and wait for it to finish",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, host, cleanup, err := backupHostEnv(g, &hf, args)
			if err != nil {
				return err
			}
			defer cleanup()
			prog := output.NewProgress(cmd.OutOrStdout(), "backup.run", host, g.json)
			env.Report = func(phase, status, msg string) { prog.Phase(phase, status, msg) }
			if err := core.RunBackup(env, host); err != nil {
				prog.Result("fail", err.Error(), exitCodeOf(err))
				return silentExit(err)
			}
			prog.Result("pass", "backup completed on "+host, 0, "host", host)
			return nil
		},
	}
	hf.register(cmd)
	return cmd
}

func newBackupStatusCmd(g *globalOptions) *cobra.Command {
	var hf hostFlags
	cmd := &cobra.Command{
		Use:   "status [server]",
		Short: "Show the server's backup timer, service state, and last run",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, host, cleanup, err := backupHostEnv(g, &hf, args)
			if err != nil {
				return err
			}
			defer cleanup()
			out, err := core.BackupStatus(env, host)
			if err != nil {
				return err
			}
			cmd.Println(out)
			return nil
		},
	}
	hf.register(cmd)
	return cmd
}
