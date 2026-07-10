package cli

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/epistola-app/portablevps/internal/adapters"
	"github.com/epistola-app/portablevps/internal/config"
	"github.com/epistola-app/portablevps/internal/core"
	"github.com/epistola-app/portablevps/internal/keystore"
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
	f.StringVar(&h.adminKey, "admin-key", ".local/ssh/cloud-admin_ed25519", "fallback path to the admin SSH private key (used when 1Password has no item)")
}

// sshAdapter builds the SSH adapter, sourcing the admin identity through the
// keystore: the 1Password agent (interactive) or an op-read temp key (headless)
// when a vault item exists, otherwise the file fallback. Returns a cleanup that
// shreds any temp key.
func (h *hostFlags) sshAdapter(g *globalOptions, ctx *config.Context, server string) (adapters.SSH, func(), error) {
	store := keystore.Store{
		Runner:    adapters.ExecRunner{},
		OpAccount: ctx.OpAccount,
		AgentSock: onePasswordAgentSock(),
	}
	mode := keystore.Headless
	if g.interactive() {
		mode = keystore.Interactive
	}
	ref := keystore.Ref{
		OpItem:   keystore.DefaultOpItem(ctx.Vault, server),
		FilePath: repoRelOrAbs(ctx.RepoRoot, h.adminKey),
		PubPath:  filepath.Join(ctx.RepoRoot, "keys", server+"-admin.pub"),
	}
	opts, cleanup, err := store.SSHIdentity(ref, mode)
	if err != nil {
		return adapters.SSH{}, func() {}, ExitError{Code: 66, Message: err.Error()}
	}
	return adapters.SSH{RepoRoot: ctx.RepoRoot, IdentityOpts: opts, Port: h.sshPort}, cleanup, nil
}

func repoRelOrAbs(root, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(root, p)
}

// onePasswordAgentSock returns the 1Password SSH agent socket (override with
// PORTABLEVPS_1P_AGENT_SOCK). Only used in interactive+vault mode.
func onePasswordAgentSock() string {
	if s := os.Getenv("PORTABLEVPS_1P_AGENT_SOCK"); s != "" {
		return s
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".1password", "agent.sock")
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

			ssh, cleanup, err := hf.sshAdapter(g, ctx, server)
			if err != nil {
				return err
			}
			defer cleanup()

			prog := output.NewProgress(cmd.OutOrStdout(), "server.deploy", server, g.json)
			env := core.DeployEnv{
				RepoRoot: ctx.RepoRoot,
				Runner:   adapters.ExecRunner{},
				Host:     ssh,
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
