package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/epistola-app/portablevps/internal/adapters"
	"github.com/epistola-app/portablevps/internal/config"
	"github.com/epistola-app/portablevps/internal/core"
	"github.com/epistola-app/portablevps/internal/keystore"
	"github.com/epistola-app/portablevps/internal/output"
	"github.com/epistola-app/portablevps/internal/secrets"
)

// confirmDestroyTarget gates a disk-wiping operation on the resource name
// (type-the-host at a TTY, --confirm <host> in CI).
func confirmDestroyTarget(g *globalOptions, cmd *cobra.Command, resource string) error {
	return g.confirmDestroy(resource, "host", cmd.InOrStdin(), cmd.ErrOrStderr())
}

// resolveSecretRef dereferences an op://, env:// reference (falls back to the
// literal on failure) — used for --password.
func resolveSecretRef(ctx *config.Context, value string) string {
	r := secrets.Resolver{Runner: adapters.ExecRunner{}, Getenv: os.Getenv, OpAccount: ctx.OpAccount, Managers: secretManagers(ctx)}
	if out, err := r.Resolve(value); err == nil {
		return out
	}
	return value
}

// secretManagers adapts the config's registered managers to the resolver's type.
func secretManagers(ctx *config.Context) []secrets.Manager {
	out := make([]secrets.Manager, 0, len(ctx.Managers))
	for _, m := range ctx.Managers {
		out = append(out, secrets.Manager{Scheme: m.Scheme, Command: m.Command})
	}
	return out
}

// provisionFlags are the install/adopt inputs.
type provisionFlags struct {
	target        string
	host          string
	sshPort       string
	disk          string // informational; the disk is set in the server config
	buildOnRemote bool
	restoreMode   bool
	// adopt-only
	loginUser  string
	initialKey string
	password   string // resolved (op:// ok)
	adminPub   string
}

// ageKeyMaterial resolves the server's age key content (1Password -> file) to
// ship to the host.
func ageKeyMaterial(ctx *config.Context, server string) (string, error) {
	store := keystore.Store{Runner: adapters.ExecRunner{}, OpAccount: ctx.OpAccount}
	ref := keystore.Ref{
		OpItem:   keystore.DefaultOpItem(ctx.Vault, server),
		FilePath: filepath.Join(ctx.RepoRoot, core.PerServerKeyRel(server)),
	}
	material, err := store.AgeMaterial(ref)
	if err != nil {
		return "", ExitError{Code: 66, Message: fmt.Sprintf("no age key for %s (run `secret init` or add the 1Password item): %v", server, err)}
	}
	return material, nil
}

// nixosAnywhereIdentity resolves the SSH identity for nixos-anywhere from the
// keystore and converts ssh -o/-i options into nixos-anywhere args.
func nixosAnywhereIdentity(g *globalOptions, ctx *config.Context, server, adminKey, adminPub string) ([]string, func(), error) {
	store := keystore.Store{Runner: adapters.ExecRunner{}, OpAccount: ctx.OpAccount}
	mode := keystore.Headless
	if g.interactive() {
		mode = keystore.Interactive
	}
	ref := resolveAdminIdentity(ctx, server, adminKey, adminPub).ref(ctx, server, adminKey == "" && os.Getenv("CLOUD_ADMIN_KEY") == "")
	opts, cleanup, err := store.SSHIdentity(ref, mode)
	if err != nil {
		return nil, func() {}, ExitError{Code: 66, Message: err.Error()}
	}
	// ssh "-o X" -> nixos-anywhere "--ssh-option X"; "-i k" stays "-i k".
	var out []string
	for i := 0; i < len(opts); i++ {
		if opts[i] == "-o" && i+1 < len(opts) {
			out = append(out, "--ssh-option", opts[i+1])
			i++
		} else {
			out = append(out, opts[i])
		}
	}
	return out, cleanup, nil
}

func newServerInstallCmd(g *globalOptions) *cobra.Command {
	var pf provisionFlags
	cmd := &cobra.Command{
		Use:   "install [server]",
		Short: "Install NixOS onto a reachable target with nixos-anywhere (destructive)",
		Long: "Provisions NixOS onto TARGET (a rescue system or fresh host reachable " +
			"over SSH), shipping the host's age key and building on the remote. This " +
			"WIPES the target disk.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server, ctx, err := serverArg(g, args)
			if err != nil {
				return err
			}
			target := pf.target
			if target == "" && pf.host != "" {
				target = pf.loginUser + "@" + pf.host
			}
			if target == "" {
				return ExitError{Code: 64, Message: "install needs --target user@host (or --host)"}
			}
			if err := confirmDestroyTarget(g, cmd, target); err != nil {
				return err
			}
			return runInstall(g, cmd, ctx, server, target, &pf)
		},
	}
	registerProvisionFlags(cmd, &pf, false)
	return cmd
}

func newServerAdoptCmd(g *globalOptions) *cobra.Command {
	var pf provisionFlags
	cmd := &cobra.Command{
		Use:   "adopt [server]",
		Short: "Adopt a foreign host: install our admin key, then install NixOS (destructive)",
		Long: "Bootstraps our admin public key onto an existing host (via --initial-key, " +
			"the SSH agent, or --password), then installs NixOS onto it. WIPES the disk.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server, ctx, err := serverArg(g, args)
			if err != nil {
				return err
			}
			if pf.host == "" {
				return ExitError{Code: 64, Message: "adopt needs --host <ip-or-name>"}
			}
			if err := confirmDestroyTarget(g, cmd, pf.host); err != nil {
				return err
			}

			prog := output.NewProgress(cmd.OutOrStdout(), "server.adopt", server, g.json)
			// 1. bootstrap the admin public key onto login_user@host.
			if err := bootstrapAdminKey(ctx, server, &pf, prog); err != nil {
				return err
			}
			// 2. install, now reachable over the admin key.
			target := pf.loginUser + "@" + pf.host
			return runInstall(g, cmd, ctx, server, target, &pf)
		},
	}
	registerProvisionFlags(cmd, &pf, true)
	return cmd
}

func newServerRepurposeCmd(g *globalOptions) *cobra.Command {
	var hf hostFlags
	var resetPaths []string
	cmd := &cobra.Command{
		Use:   "repurpose [server]",
		Short: "Switch a running host to a different server config in place (no reinstall)",
		Long: "Repoints an already-portablevps host at a DIFFERENT logical server: " +
			"stops apps, swaps the host age key, optionally clears data dirs, then " +
			"nixos-rebuild switch. NetBird/machine state persist. Destructive (data).",
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
			if err := confirmDestroyTarget(g, cmd, host); err != nil {
				return err
			}
			ageKey, err := ageKeyMaterial(ctx, server)
			if err != nil {
				return err
			}
			ssh, cleanup, err := hf.sshAdapter(g, ctx, server)
			if err != nil {
				return err
			}
			defer cleanup()

			prog := output.NewProgress(cmd.OutOrStdout(), "server.repurpose", server, g.json)
			env := core.RepurposeEnv{
				RepoRoot:       ctx.RepoRoot,
				Host:           ssh,
				Stream:         adapters.ExecRunner{},
				AgeKeyMaterial: ageKey,
				Report:         func(phase, status, msg string) { prog.Phase(phase, status, msg) },
			}
			if err := core.Repurpose(env, core.RepurposeOpts{Server: server, Host: host, ResetPaths: resetPaths}); err != nil {
				prog.Result("fail", err.Error(), exitCodeOf(err))
				return silentExit(err)
			}
			prog.Result("pass", host+" now runs .#"+server, 0, "host", host)
			return nil
		},
	}
	hf.register(cmd)
	cmd.Flags().StringSliceVar(&resetPaths, "reset-path", nil, "data dir to clear before switching (repeatable; must be under /data or /var/lib)")
	return cmd
}

func registerProvisionFlags(cmd *cobra.Command, pf *provisionFlags, adopt bool) {
	f := cmd.Flags()
	f.StringVar(&pf.target, "target", "", "install target, e.g. root@1.2.3.4")
	f.StringVar(&pf.host, "host", "", "host address (with --login-user forms the target)")
	f.StringVar(&pf.sshPort, "ssh-port", "22", "SSH port")
	f.BoolVar(&pf.buildOnRemote, "build-on-remote", true, "build the system closure on the target (cross-arch)")
	f.BoolVar(&pf.restoreMode, "restore", false, "install the <server>-restore profile")
	f.StringVar(&pf.adminPub, "admin-pubkey", "", "admin public key override shipped/authorized on the host (default: per-server/cloud-admin fallback)")
	if adopt {
		f.StringVar(&pf.loginUser, "login-user", "root", "existing login user on the foreign host")
		f.StringVar(&pf.initialKey, "initial-key", "", "initial SSH private key for the foreign host (else the agent)")
		f.StringVar(&pf.password, "password", "", "root password for the foreign host (else key/agent); op:// ok")
	} else {
		f.StringVar(&pf.loginUser, "login-user", "root", "login user for the install target")
	}
}

func runInstall(g *globalOptions, cmd *cobra.Command, ctx *config.Context, server, target string, pf *provisionFlags) error {
	ageKey, err := ageKeyMaterial(ctx, server)
	if err != nil {
		return err
	}
	identity, cleanup, err := nixosAnywhereIdentity(g, ctx, server, "", pf.adminPub)
	if err != nil {
		return err
	}
	defer cleanup()

	prog := output.NewProgress(cmd.OutOrStdout(), "server.install", server, g.json)
	env := core.InstallEnv{
		FlakeDir: ctx.RepoRoot,
		Stream:   adapters.ExecRunner{},
		Runner:   adapters.ExecRunner{},
		Report:   func(phase, status, msg string) { prog.Phase(phase, status, msg) },
	}
	if err := core.Install(env, core.InstallOpts{
		Server:         server,
		Target:         target,
		SSHPort:        pf.sshPort,
		AgeKeyMaterial: ageKey,
		IdentityArgs:   identity,
		BuildOnRemote:  pf.buildOnRemote,
		RestoreMode:    pf.restoreMode,
	}); err != nil {
		prog.Result("fail", err.Error(), exitCodeOf(err))
		return silentExit(err)
	}
	prog.Result("pass", server+" installed on "+target, 0, "target", target)
	return nil
}

// bootstrapAdminKey installs the admin public key onto login_user@host over a
// one-off credential (initial key, agent, or password).
func bootstrapAdminKey(ctx *config.Context, server string, pf *provisionFlags, prog *output.Progress) error {
	pubPath := repoRelOrAbs(ctx.RepoRoot, resolveAdminIdentity(ctx, server, "", pf.adminPub).publicRel)
	pub, err := os.ReadFile(pubPath)
	if err != nil {
		return ExitError{Code: 66, Message: fmt.Sprintf("admin public key not found: %s", pubPath)}
	}
	target := pf.loginUser + "@" + pf.host
	remote := "set -eu; mkdir -p ~/.ssh; chmod 700 ~/.ssh; touch ~/.ssh/authorized_keys; chmod 600 ~/.ssh/authorized_keys; " +
		`key="$(cat)"; grep -qxF "$key" ~/.ssh/authorized_keys || printf '%s\n' "$key" >> ~/.ssh/authorized_keys`
	sshOpts := []string{"-p", pf.sshPort, "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "ConnectTimeout=20"}

	var c *exec.Cmd
	switch {
	case pf.password != "":
		pw := resolveSecretRef(ctx, pf.password)
		args := append([]string{"-e", "ssh"}, sshOpts...)
		c = exec.Command("sshpass", append(args, target, remote)...)
		c.Env = append(os.Environ(), "SSHPASS="+pw)
	case pf.initialKey != "":
		args := append([]string{"-i", repoRelOrAbs(ctx.RepoRoot, pf.initialKey), "-o", "IdentitiesOnly=yes"}, sshOpts...)
		c = exec.Command("ssh", append(args, target, remote)...)
		c.Env = os.Environ()
	default: // agent / default
		c = exec.Command("ssh", append(sshOpts, target, remote)...)
		c.Env = os.Environ()
	}
	c.Stdin = strings.NewReader(strings.TrimSpace(string(pub)) + "\n")
	c.Stderr = os.Stderr
	prog.Phase("bootstrap", "run", "installing admin key on "+target)
	if err := c.Run(); err != nil {
		prog.Phase("bootstrap", "fail", err.Error())
		return ExitError{Code: 70, Message: fmt.Sprintf("bootstrapping admin key on %s: %v", target, err)}
	}
	prog.Phase("bootstrap", "ok", "")
	return nil
}
