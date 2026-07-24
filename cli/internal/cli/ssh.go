package cli

import (
	"errors"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

// newSSHCmd opens an operator SSH session using the same server, mesh-host, and
// admin-key resolution as deploy/backup commands.
func newSSHCmd(g *globalOptions) *cobra.Command {
	var hf hostFlags
	cmd := &cobra.Command{
		Use:   "ssh [server] [-- remote-command...]",
		Short: "Open an admin SSH session to a server",
		Long: "Open an admin SSH session to a server using the configured mesh " +
			"hostname and per-server admin key. Extra arguments after -- are " +
			"passed to ssh as the remote command.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			serverArgs, remoteCommand := splitSSHArgs(args, cmd.ArgsLenAtDash(), g.serverFlag != "")
			if len(serverArgs) > 1 {
				return ExitError{Code: 64, Message: "too many server arguments"}
			}
			server, ctx, err := serverArg(g, serverArgs)
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

			c := exec.Command("ssh", ssh.CommandArgs(host, remoteCommand...)...)
			c.Stdin = os.Stdin
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			if err := c.Run(); err != nil {
				var exit *exec.ExitError
				if errors.As(err, &exit) {
					return ExitError{Code: exit.ExitCode(), Message: ""}
				}
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&g.serverFlag, "server", "",
		"target server (default: default_server in portablevps.toml)")
	hf.register(cmd)
	return cmd
}

func splitSSHArgs(args []string, dashAt int, hasServerFlag bool) (serverArgs []string, remoteCommand []string) {
	if dashAt >= 0 {
		return args[:dashAt], args[dashAt:]
	}
	if hasServerFlag {
		return nil, args
	}
	if len(args) <= 1 {
		return args, nil
	}
	return args[:1], args[1:]
}
