package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/sdegroot/portablevps/internal/adapters"
	"github.com/sdegroot/portablevps/internal/config"
	"github.com/sdegroot/portablevps/internal/core"
	"github.com/sdegroot/portablevps/internal/output"
)

// newTestCmd is the validation noun: disaster-recovery drills that prove
// backup/restore actually works — locally on throwaway QEMU VMs (no real infra),
// or against real hosts when you want to rehearse on production-like hardware.
func newTestCmd(g *globalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Validate the system (disaster-recovery drills, local or remote)",
	}
	cmd.PersistentFlags().StringVar(&g.serverFlag, "server", "",
		"target server (default: default_server in portablevps.toml)")
	cmd.AddCommand(newDRCommand(g, "dr [server]", false))
	return cmd
}

type drFlags struct {
	mode           string
	portablevpsDir string
	sourceHost     string
	restoreHost    string
	sourceServer   string
	restoreServer  string
	sshPort        string
	adminKey       string
}

func newDRCmd(g *globalOptions) *cobra.Command {
	return newDRCommand(g, "dr [server]", true)
}

func newDRCommand(g *globalOptions, use string, addServerFlag bool) *cobra.Command {
	flags := drFlags{sshPort: "22"}
	cmd := &cobra.Command{
		Use:   use,
		Short: "Disaster-recovery drill: prove backup/restore (local QEMU, or remote hosts)",
		Long: "Proves the server's backups actually restore, end to end.\n\n" +
			"Default (local): boots two throwaway QEMU VMs sharing the monorepo, runs the " +
			"backup->restore->verify cycle against the <server>-local-vm config, and tears " +
			"everything down — no real infrastructure touched.\n\n" +
			"Remote (--source-server + --restore-server, or --source-host + --restore-host): " +
			"runs the SAME proof against real hosts — " +
			"seed markers into PostgreSQL, every declared backup path, and app-owned hooks; " +
			"run the production backup service; restore onto the restore host; verify every " +
			"declared component; and publish the restore-drill metric. The source is only seeded; the " +
			"restore host's data is WIPED, so it should be a spare/candidate box.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server, ctx, err := serverArg(g, args)
			if err != nil {
				return err
			}
			mode, err := resolveDRMode(flags)
			if err != nil {
				return err
			}
			if mode == "remote" {
				return runRemoteDR(g, cmd, ctx, server, flags)
			}
			return runLocalDR(cmd, ctx, server, flags.portablevpsDir)
		},
	}
	if addServerFlag {
		cmd.Flags().StringVar(&g.serverFlag, "server", "",
			"target server (default: default_server in portablevps.toml)")
	}
	cmd.Flags().StringVar(&flags.mode, "mode", "", "DR mode: qemu or remote (default: qemu, remote when source/restore flags are set)")
	cmd.Flags().StringVar(&flags.portablevpsDir, "portablevps-dir", "",
		"(local mode) path to the portablevps checkout providing scripts/ (default: the ../portablevps sibling)")
	cmd.Flags().StringVar(&flags.sourceServer, "source-server", "", "(remote mode) live source server name; resolves to <server>.<mesh_domain> and uses that server's admin key")
	cmd.Flags().StringVar(&flags.restoreServer, "restore-server", "", "(remote mode) restore target server name; resolves to <server>.<mesh_domain> and uses that server's admin key")
	cmd.Flags().StringVar(&flags.sourceHost, "source-host", "", "(remote mode) live host to seed a marker on and back up")
	cmd.Flags().StringVar(&flags.restoreHost, "restore-host", "", "(remote mode) host to restore onto and verify (its data is wiped)")
	cmd.Flags().StringVar(&flags.sshPort, "ssh-port", "22", "admin SSH port (remote mode)")
	cmd.Flags().StringVar(&flags.adminKey, "admin-key", "", "single admin SSH private key override in remote mode (default: per-server key resolution)")
	return cmd
}

// runRemoteDR seeds + backs up the source, then restores onto the restore host
// and verifies the marker — the real-infrastructure DR proof.
func runRemoteDR(g *globalOptions, cmd *cobra.Command, ctx *config.Context, server string, flags drFlags) error {
	sourceHost, restoreHost, err := resolveDRRemoteHosts(ctx, flags)
	if err != nil {
		return err
	}
	if sourceHost == "" || restoreHost == "" {
		return ExitError{Code: 64, Message: "remote DR needs source and restore targets (--source-server/--restore-server or --source-host/--restore-host)"}
	}
	// The restore host is rebuilt from the backup — its data is wiped.
	if err := confirmDestroyTarget(g, cmd, restoreHost); err != nil {
		return err
	}
	hostRunner, cleanup, err := drHostRunner(g, ctx, server, flags, sourceHost, restoreHost)
	if err != nil {
		return err
	}
	defer cleanup()

	prog := output.NewProgress(cmd.OutOrStdout(), "test.dr", server, g.json)
	env := core.ServiceEnv{
		RepoRoot: ctx.RepoRoot,
		Host:     hostRunner,
		Stream:   adapters.ExecRunner{},
		Report:   func(phase, status, msg string) { prog.Phase(phase, status, msg) },
	}
	marker, err := core.RestoreDrill(env, core.DrillOpts{Server: server, SourceHost: sourceHost, RestoreHost: restoreHost})
	if err != nil {
		prog.Result("fail", err.Error(), exitCodeOf(err))
		return silentExit(err)
	}
	prog.Result("pass",
		fmt.Sprintf("restored %s onto %s and verified marker %s", sourceHost, restoreHost, marker),
		0, "marker", marker)
	return nil
}

func resolveDRMode(flags drFlags) (string, error) {
	remoteRequested := flags.sourceHost != "" || flags.restoreHost != "" || flags.sourceServer != "" || flags.restoreServer != ""
	switch flags.mode {
	case "", "auto":
		if remoteRequested {
			return "remote", nil
		}
		return "qemu", nil
	case "qemu":
		if remoteRequested {
			return "", ExitError{Code: 64, Message: "--mode qemu cannot be combined with remote source/restore flags"}
		}
		return "qemu", nil
	case "remote":
		return "remote", nil
	default:
		return "", ExitError{Code: 64, Message: "unknown DR mode " + flags.mode + " (expected qemu or remote)"}
	}
}

func resolveDRRemoteHosts(ctx *config.Context, flags drFlags) (sourceHost, restoreHost string, err error) {
	sourceHost, err = resolveDRRemoteHost(ctx, "source", flags.sourceServer, flags.sourceHost)
	if err != nil {
		return "", "", err
	}
	restoreHost, err = resolveDRRemoteHost(ctx, "restore", flags.restoreServer, flags.restoreHost)
	if err != nil {
		return "", "", err
	}
	return sourceHost, restoreHost, nil
}

func resolveDRRemoteHost(ctx *config.Context, role, serverName, host string) (string, error) {
	if serverName != "" && host != "" {
		return "", ExitError{Code: 64, Message: "pass either --" + role + "-server or --" + role + "-host, not both"}
	}
	if host != "" {
		return host, nil
	}
	if serverName == "" {
		return "", nil
	}
	resolved := defaultMeshHost(serverName, ctx)
	if resolved == "" {
		return "", ExitError{Code: 64, Message: "no mesh host could be derived for " + role + " server " + serverName + "; pass --" + role + "-host"}
	}
	return resolved, nil
}

// runLocalDR boots two throwaway QEMU VMs and runs the backup/restore proof
// against them via the existing scripts/qemu-dr-run.sh harness.
func runLocalDR(cmd *cobra.Command, ctx *config.Context, server, portablevpsDir string) error {
	scriptsDir, err := resolvePortablevpsDir(ctx, portablevpsDir)
	if err != nil {
		return err
	}
	script := filepath.Join(scriptsDir, "scripts", "qemu-dr-run.sh")
	if !fileExistsCli(script) {
		return ExitError{Code: 66, Message: fmt.Sprintf("DR harness not found: %s (pass --portablevps-dir to the portablevps checkout)", script)}
	}
	// The harness shares the monorepo root into the guests at /host so a
	// consumer's `path:../portablevps` flake input resolves; the consumer flake is
	// then found at /host/<consumer-basename>.
	env := map[string]string{
		"SHARE":     filepath.Dir(ctx.RepoRoot),
		"CONFIG":    server + "-local-vm",
		"FLAKE_DIR": "/host/" + filepath.Base(ctx.RepoRoot),
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "test dr: local QEMU rehearsal of %s (config %s-local-vm) via %s\n", server, server, script)
	if err := (adapters.ExecRunner{}).Stream(scriptsDir, env, "bash", filepath.Join("scripts", "qemu-dr-run.sh")); err != nil {
		return ExitError{Code: 70, Message: fmt.Sprintf("local DR rehearsal failed: %v", err)}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "test dr: %s backup/restore validated locally\n", server)
	return nil
}

// resolvePortablevpsDir finds the portablevps checkout that holds the QEMU
// scripts. An explicit --portablevps-dir (or $PORTABLEVPS_DIR) is authoritative:
// if given but wrong, it errors rather than silently falling back. Otherwise it
// auto-detects the ../portablevps sibling of the consumer repo (monorepo layout).
func resolvePortablevpsDir(ctx *config.Context, override string) (string, error) {
	explicit := override
	if explicit == "" {
		explicit = os.Getenv("PORTABLEVPS_DIR")
	}
	if explicit != "" {
		abs, err := filepath.Abs(explicit)
		if err == nil && fileExistsCli(filepath.Join(abs, "scripts", "qemu-dr-run.sh")) {
			return abs, nil
		}
		return "", ExitError{Code: 66, Message: fmt.Sprintf("no portablevps scripts under %q (expected scripts/qemu-dr-run.sh)", explicit)}
	}
	sibling := filepath.Join(filepath.Dir(ctx.RepoRoot), "portablevps")
	abs, err := filepath.Abs(sibling)
	if err == nil && fileExistsCli(filepath.Join(abs, "scripts", "qemu-dr-run.sh")) {
		return abs, nil
	}
	return "", ExitError{Code: 66, Message: "could not locate the portablevps checkout (its scripts/); pass --portablevps-dir"}
}
