package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/epistola-app/portablevps/internal/adapters"
	"github.com/epistola-app/portablevps/internal/config"
	"github.com/epistola-app/portablevps/internal/core"
	"github.com/epistola-app/portablevps/internal/output"
)

// newServerCmd is the machine-identity noun: operations that act on a box.
func newServerCmd(g *globalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Provision, deploy to, and operate server machines",
	}
	cmd.PersistentFlags().StringVar(&g.serverFlag, "server", "",
		"target server (default: default_server in portablevps.toml)")
	cmd.AddCommand(newServerDeployCmd(g))
	return cmd
}

// hostFlags are the SSH-connection flags shared by host-driving commands.
type hostFlags struct {
	host     string
	sshPort  string
	adminKey string
}

func (h *hostFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&h.host, "host", "", "the running host's admin address (default: <server>.<mesh>)")
	f.StringVar(&h.sshPort, "ssh-port", "22", "admin SSH port")
	f.StringVar(&h.adminKey, "admin-key", ".local/ssh/cloud-admin_ed25519", "path to the cloud-admin SSH private key")
}

func (h *hostFlags) sshAdapter(repoRoot string) adapters.SSH {
	return adapters.SSH{RepoRoot: repoRoot, AdminKey: h.adminKey, Port: h.sshPort}
}

func newServerDeployCmd(g *globalOptions) *cobra.Command {
	var hf hostFlags
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "deploy [server]",
		Short: "Push the committed config to a running server in place (nixos-rebuild switch)",
		Long: "Deploys the current committed flake to an already-running host: an " +
			"in-place nixos-rebuild switch over admin SSH, built on the remote. No " +
			"identity or data changes (unlike repurpose). Use --dry-run to build and " +
			"show what would change without switching.",
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

			prog := output.NewProgress(cmd.OutOrStdout(), "server.deploy", server, g.json)
			env := core.DeployEnv{
				RepoRoot: ctx.RepoRoot,
				Runner:   adapters.ExecRunner{},
				Host:     hf.sshAdapter(ctx.RepoRoot),
				Report:   func(phase, status, msg string) { prog.Phase(phase, status, msg) },
				DryRun:   dryRun,
			}
			if err := core.Deploy(env, server, host); err != nil {
				prog.Result("fail", err.Error(), exitCodeOf(err))
				return silentExit(err)
			}
			msg := host + " now runs .#" + server
			if dryRun {
				msg = "dry run complete for .#" + server + " on " + host + " (no changes made)"
			}
			prog.Result("pass", msg, 0, "host", host, "dryRun", dryRun)
			return nil
		},
	}
	hf.register(cmd)
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "build and show what would change, but do not switch")
	return cmd
}

// defaultMeshHost derives <server>.<mesh-zone> when a mesh DNS zone is known,
// matching the Taskfile's HOST default of "<server>.epistola.int".
func defaultMeshHost(server string, ctx *config.Context) string {
	// The internal mesh name is <server> under the network's peer domain. When a
	// dns_zone is configured we use "<server>.<zone>"; otherwise the operator must
	// pass --host.
	if ctx != nil && ctx.DNSZone != "" {
		return server + "." + ctx.DNSZone
	}
	return ""
}

// exitCodeOf extracts an ExitCode() from an error, defaulting to 1.
func exitCodeOf(err error) int {
	var coder interface{ ExitCode() int }
	if errors.As(err, &coder) {
		return coder.ExitCode()
	}
	return 1
}

// silentExit wraps an error as an ExitError with the same code but an empty
// message, so a command that already rendered the failure (via progress) does
// not double-print it.
func silentExit(err error) error {
	return ExitError{Code: exitCodeOf(err), Message: ""}
}
