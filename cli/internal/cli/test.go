package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/epistola-app/portablevps/internal/adapters"
	"github.com/epistola-app/portablevps/internal/config"
	"github.com/epistola-app/portablevps/internal/core"
	"github.com/epistola-app/portablevps/internal/output"
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
	cmd.AddCommand(newTestDRCmd(g))
	return cmd
}

func newTestDRCmd(g *globalOptions) *cobra.Command {
	var portablevpsDir, sourceHost, restoreHost, sshPort, adminKey string
	cmd := &cobra.Command{
		Use:   "dr [server]",
		Short: "Disaster-recovery drill: prove backup/restore (local QEMU, or remote hosts)",
		Long: "Proves the server's backups actually restore, end to end.\n\n" +
			"Default (local): boots two throwaway QEMU VMs sharing the monorepo, runs the " +
			"backup->restore->verify cycle against the <server>-local-vm config, and tears " +
			"everything down — no real infrastructure touched.\n\n" +
			"Remote (--source-host + --restore-host): runs the SAME proof against real hosts — " +
			"seed a marker in the source's live data, back it up, restore onto the restore host, " +
			"and verify the marker survived. The source is only seeded (non-destructive); the " +
			"restore host's data is WIPED, so it should be a spare/candidate box.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server, ctx, err := serverArg(g, args)
			if err != nil {
				return err
			}
			if sourceHost != "" || restoreHost != "" {
				return runRemoteDR(g, cmd, ctx, server, sshPort, adminKey, sourceHost, restoreHost)
			}
			return runLocalDR(cmd, ctx, server, portablevpsDir)
		},
	}
	cmd.Flags().StringVar(&portablevpsDir, "portablevps-dir", "",
		"(local mode) path to the portablevps checkout providing scripts/ (default: the ../portablevps sibling)")
	cmd.Flags().StringVar(&sourceHost, "source-host", "", "(remote mode) live host to seed a marker on and back up")
	cmd.Flags().StringVar(&restoreHost, "restore-host", "", "(remote mode) host to restore onto and verify (its data is wiped)")
	cmd.Flags().StringVar(&sshPort, "ssh-port", "22", "admin SSH port (remote mode)")
	cmd.Flags().StringVar(&adminKey, "admin-key", ".local/ssh/cloud-admin_ed25519", "fallback admin SSH key path (remote mode)")
	return cmd
}

// runRemoteDR seeds + backs up the source, then restores onto the restore host
// and verifies the marker — the real-infrastructure DR proof.
func runRemoteDR(g *globalOptions, cmd *cobra.Command, ctx *config.Context, server, sshPort, adminKey, sourceHost, restoreHost string) error {
	if sourceHost == "" || restoreHost == "" {
		return ExitError{Code: 64, Message: "remote DR needs both --source-host and --restore-host"}
	}
	// The restore host is rebuilt from the backup — its data is wiped.
	if err := confirmDestroyTarget(g, cmd, restoreHost); err != nil {
		return err
	}
	hf := hostFlags{sshPort: sshPort, adminKey: adminKey}
	ssh, cleanup, err := hf.sshAdapter(g, ctx, server)
	if err != nil {
		return err
	}
	defer cleanup()

	prog := output.NewProgress(cmd.OutOrStdout(), "test.dr", server, g.json)
	env := core.ServiceEnv{
		RepoRoot: ctx.RepoRoot,
		Host:     ssh,
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
