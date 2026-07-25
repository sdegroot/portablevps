package cli

import (
	"errors"
	"os"
	"path/filepath"
	"time"

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
	cmd.AddCommand(
		newServerListCmd(g),
		newServerDeployCmd(g),
		newServerRebootCmd(g),
		newServerRollbackCmd(g),
		newServerInstallCmd(g),
		newServerAdoptCmd(g),
		newServerRepurposeCmd(g),
	)
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
	f.StringVar(&h.host, "host", "", "the running host's admin address (default: <server>.<network.mesh_domain>)")
	f.StringVar(&h.sshPort, "ssh-port", "22", "admin SSH port")
	f.StringVar(&h.adminKey, "admin-key", "", "admin SSH private key override (default: 1Password/per-server file/cloud-admin fallback)")
}

// sshAdapter builds the SSH adapter, sourcing the admin identity through the
// keystore: the 1Password agent (interactive) or an op-read temp key (headless)
// when a vault item exists, otherwise the file fallback. Returns a cleanup that
// shreds any temp key.
func (h *hostFlags) sshAdapter(g *globalOptions, ctx *config.Context, server string) (adapters.SSH, func(), error) {
	store := keystore.Store{
		Runner:    adapters.ExecRunner{},
		OpAccount: ctx.OpAccount,
	}
	mode := keystore.Headless
	if g.interactive() {
		mode = keystore.Interactive
	}
	ref := resolveAdminIdentity(ctx, server, h.adminKey, "").ref(ctx, server, h.adminKey == "" && os.Getenv("CLOUD_ADMIN_KEY") == "")
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

func newServerRebootCmd(g *globalOptions) *cobra.Command {
	var hf hostFlags
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "reboot [server]",
		Short: "Reboot a server and verify the new boot and system generation",
		Long: "Reboots a running server over admin SSH, waits for a distinct boot " +
			"ID, then verifies that /run/booted-system matches /run/current-system " +
			"and that no system services are failed.",
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
			if timeout <= 0 {
				return ExitError{Code: 64, Message: "--timeout must be greater than zero"}
			}

			ssh, cleanup, err := hf.sshAdapter(g, ctx, server)
			if err != nil {
				return err
			}
			defer cleanup()

			const pollDelay = 2 * time.Second
			attempts := int((timeout + pollDelay - 1) / pollDelay)
			prog := output.NewProgress(cmd.OutOrStdout(), "server.reboot", server, g.json)
			result, err := core.Reboot(core.RebootEnv{
				Host:     ssh,
				Report:   func(phase, status, msg string) { prog.Phase(phase, status, msg) },
				Attempts: attempts,
				Delay:    pollDelay,
			}, host)
			if err != nil {
				prog.Result("fail", err.Error(), exitCodeOf(err))
				return silentExit(err)
			}
			prog.Result("pass", host+" completed a verified reboot", 0,
				"host", host, "bootID", result.BootID, "system", result.System)
			return nil
		},
	}
	hf.register(cmd)
	cmd.Flags().DurationVar(&timeout, "timeout", 3*time.Minute, "maximum time to wait for a changed boot ID")
	return cmd
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
			"show what would change without switching.\n\n" +
			"[server] may be a bare name or a path to its definition file " +
			"(servers/<name>.nix), so you can shell tab-complete it.",
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

func newServerRollbackCmd(g *globalOptions) *cobra.Command {
	var hf hostFlags
	var list bool
	var toGen int
	cmd := &cobra.Command{
		Use:   "rollback [server]",
		Short: "Activate a previous NixOS generation on the server (config rollback)",
		Long: "Rolls the running server back to a previous NixOS generation (a " +
			"config/version rollback, distinct from a data restore). --list shows " +
			"the available generations; --to <n> switches to a specific one.",
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

			env := core.RollbackEnv{Host: ssh}
			if list {
				out, err := core.ListGenerations(env, host)
				if err != nil {
					return err
				}
				cmd.Println(out)
				return nil
			}

			prog := output.NewProgress(cmd.OutOrStdout(), "server.rollback", server, g.json)
			env.Report = func(phase, status, msg string) { prog.Phase(phase, status, msg) }
			if err := core.Rollback(env, host, toGen); err != nil {
				prog.Result("fail", err.Error(), exitCodeOf(err))
				return silentExit(err)
			}
			prog.Result("pass", host+" rolled back", 0, "host", host)
			return nil
		},
	}
	hf.register(cmd)
	cmd.Flags().BoolVar(&list, "list", false, "list available NixOS generations instead of rolling back")
	cmd.Flags().IntVar(&toGen, "to", 0, "switch to a specific generation number (default: the previous one)")
	return cmd
}

// defaultMeshHost derives the admin address "<server>.<mesh_domain>" — the
// NetBird peer FQDN — matching the Taskfile's HOST default of
// "<server>.epistola.int".
func defaultMeshHost(server string, ctx *config.Context) string {
	// The admin address is the mesh PEER domain (network.mesh_domain, e.g.
	// "epistola.int"), NOT the service dns_zone (e.g. "int.epistola.io") — those
	// are distinct: dns_zone holds published app hostnames, mesh_domain is where
	// NetBird resolves peers for operator SSH. When mesh_domain is unset the
	// operator must pass --host.
	if ctx != nil && ctx.MeshDomain != "" {
		return server + "." + ctx.MeshDomain
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
