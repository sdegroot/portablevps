package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/epistola-app/portablevps/internal/adapters"
	"github.com/epistola-app/portablevps/internal/config"
)

// newDevCmd is the local-harness noun: QEMU-backed disaster-recovery rehearsals
// that need no real infrastructure. Apple-Silicon today.
func newDevCmd(g *globalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dev",
		Short: "Local QEMU harness (disaster-recovery rehearsals, no real infra)",
	}
	cmd.PersistentFlags().StringVar(&g.serverFlag, "server", "",
		"target server (default: default_server in portablevps.toml)")
	cmd.AddCommand(newDevDRCmd(g))
	return cmd
}

func newDevDRCmd(g *globalOptions) *cobra.Command {
	var portablevpsDir string
	cmd := &cobra.Command{
		Use:   "dr [server]",
		Short: "Boot two local VMs, validate a server's backup/restore, tear down",
		Long: "One-command local disaster-recovery rehearsal: boots two throwaway QEMU " +
			"VMs sharing the monorepo root, validates the server's <server>-local-vm " +
			"config's backup/restore against them, then tears the VMs and test MinIO " +
			"down. Requires the portablevps repo (for its scripts/) and QEMU locally.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server, ctx, err := serverArg(g, args)
			if err != nil {
				return err
			}
			scriptsDir, err := resolvePortablevpsDir(ctx, portablevpsDir)
			if err != nil {
				return err
			}
			script := filepath.Join(scriptsDir, "scripts", "qemu-dr-run.sh")
			if !fileExistsCli(script) {
				return ExitError{Code: 66, Message: fmt.Sprintf("DR harness not found: %s (pass --portablevps-dir to the portablevps checkout)", script)}
			}

			// The DR script shares the monorepo root into the guests at /host, so a
			// consumer's `path:../portablevps` flake input resolves inside the VM. The
			// consumer flake is then found at /host/<consumer-basename>.
			monorepoRoot := filepath.Dir(ctx.RepoRoot)
			flakeDir := "/host/" + filepath.Base(ctx.RepoRoot)
			env := map[string]string{
				"SHARE":     monorepoRoot,
				"CONFIG":    server + "-local-vm",
				"FLAKE_DIR": flakeDir,
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "dev dr: rehearsing %s (config %s-local-vm) via %s\n", server, server, script)
			if err := (adapters.ExecRunner{}).Stream(scriptsDir, env, "bash", filepath.Join("scripts", "qemu-dr-run.sh")); err != nil {
				return ExitError{Code: 70, Message: fmt.Sprintf("DR rehearsal failed: %v", err)}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "dev dr: %s backup/restore validated\n", server)
			return nil
		},
	}
	cmd.Flags().StringVar(&portablevpsDir, "portablevps-dir", "",
		"path to the portablevps checkout providing scripts/ (default: the ../portablevps sibling)")
	return cmd
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
