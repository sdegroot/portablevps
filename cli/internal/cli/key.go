package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/epistola-app/portablevps/internal/adapters"
	"github.com/epistola-app/portablevps/internal/config"
	"github.com/epistola-app/portablevps/internal/core"
	"github.com/epistola-app/portablevps/internal/keystore"
)

// newKeyCmd escrows per-server key material to/from a password manager, so the
// keys aren't single-copy on the operator laptop + host. 1Password today; the
// item field layout (`age-key`) matches exactly what the keystore reads back.
func newKeyCmd(g *globalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "key",
		Short: "Generate and escrow per-server keys",
	}
	cmd.PersistentFlags().StringVar(&g.serverFlag, "server", "",
		"target server (default: default_server in portablevps.toml)")
	cmd.AddCommand(newKeyGenerateCmd(g), newKeyImportCmd(g), newKeyExportCmd(g))
	return cmd
}

// ageKeyField is the concealed field the keystore reads as op://<vault>/<server>/age-key.
func ageKeyField(ageKey string) map[string]any {
	return map[string]any{"id": "age-key", "label": "age-key", "type": "CONCEALED", "value": ageKey}
}

type adminKeypair struct {
	privateRel string
	publicRel  string
}

var runSSHKeygen = func(privatePath, comment string) error {
	_, err := adapters.ExecRunner{}.Run("", "ssh-keygen",
		"-t", "ed25519",
		"-a", "64",
		"-N", "",
		"-C", comment,
		"-f", privatePath,
	)
	return err
}

func serverAdminKeypair(server string) adminKeypair {
	return adminKeypair{
		privateRel: perServerAdminPrivateRel(server),
		publicRel:  perServerAdminPublicRel(server),
	}
}

func ensureServerAdminKeypair(ctx *config.Context, server string) (created bool, pair adminKeypair, err error) {
	pair = serverAdminKeypair(server)
	privatePath := filepath.Join(ctx.RepoRoot, pair.privateRel)
	publicPath := filepath.Join(ctx.RepoRoot, pair.publicRel)
	privateExists := fileExistsCli(privatePath)
	publicExists := fileExistsCli(publicPath)
	switch {
	case privateExists && publicExists:
		return false, pair, nil
	case !privateExists && !publicExists:
		pair, err := generateServerAdminKeypair(ctx, server, false)
		return true, pair, err
	default:
		return false, pair, ExitError{
			Code: 73,
			Message: fmt.Sprintf(
				"partial admin SSH keypair for %s: private exists=%t public exists=%t; run `portablevps key generate %s --force` to replace it",
				server, privateExists, publicExists, server,
			),
		}
	}
}

func generateServerAdminKeypair(ctx *config.Context, server string, force bool) (adminKeypair, error) {
	pair := serverAdminKeypair(server)
	privatePath := filepath.Join(ctx.RepoRoot, pair.privateRel)
	publicPath := filepath.Join(ctx.RepoRoot, pair.publicRel)
	privateExists := fileExistsCli(privatePath)
	publicExists := fileExistsCli(publicPath)
	if (privateExists || publicExists) && !force {
		return pair, ExitError{
			Code:    73,
			Message: fmt.Sprintf("admin SSH keypair for %s already exists; pass --force to replace it", server),
		}
	}
	if err := os.MkdirAll(filepath.Dir(privatePath), 0o700); err != nil {
		return pair, ExitError{Code: 70, Message: err.Error()}
	}
	if err := os.MkdirAll(filepath.Dir(publicPath), 0o755); err != nil {
		return pair, ExitError{Code: 70, Message: err.Error()}
	}
	if force {
		_ = os.Remove(privatePath)
		_ = os.Remove(publicPath)
		_ = os.Remove(privatePath + ".pub")
	}
	comment := "portablevps-" + server + "-admin"
	if err := runSSHKeygen(privatePath, comment); err != nil {
		return pair, ExitError{Code: 70, Message: fmt.Sprintf("ssh-keygen for %s: %v", server, err)}
	}
	generatedPub := privatePath + ".pub"
	pub, err := os.ReadFile(generatedPub)
	if err != nil {
		return pair, ExitError{Code: 70, Message: fmt.Sprintf("reading generated public key: %v", err)}
	}
	if err := os.WriteFile(publicPath, pub, 0o644); err != nil {
		return pair, ExitError{Code: 70, Message: err.Error()}
	}
	if err := os.Remove(generatedPub); err != nil {
		return pair, ExitError{Code: 70, Message: fmt.Sprintf("removing temporary public key %s: %v", generatedPub, err)}
	}
	if err := os.Chmod(privatePath, 0o600); err != nil {
		return pair, ExitError{Code: 70, Message: err.Error()}
	}
	if err := os.Chmod(publicPath, 0o644); err != nil {
		return pair, ExitError{Code: 70, Message: err.Error()}
	}
	return pair, nil
}

func shouldAutoGenerateAdminKey(adminPub string) bool {
	return adminPub == "" && os.Getenv("CLOUD_ADMIN_KEY") == "" && os.Getenv("CLOUD_ADMIN_PUBKEY") == ""
}

func newKeyGenerateCmd(g *globalOptions) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "generate [server]",
		Short: "Generate a server's local admin SSH keypair",
		Long: "Creates the per-server admin SSH keypair used by install/adopt and " +
			"normal admin SSH fallback:\n" +
			"  .local/ssh/servers/<server>_ed25519\n" +
			"  keys/servers/<server>-admin.pub\n\n" +
			"Refuses to overwrite existing files without --force.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server, ctx, err := serverArg(g, args)
			if err != nil {
				return err
			}
			pair, err := generateServerAdminKeypair(ctx, server, force)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "created private key: %s\n", pair.privateRel)
			fmt.Fprintf(out, "created public key:  %s\n", pair.publicRel)
			warnIfDirty(cmd.ErrOrStderr(), ctx.RepoRoot)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing per-server admin SSH keypair")
	return cmd
}

func newKeyImportCmd(g *globalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import [server]",
		Short: "Escrow a server's local age key into its 1Password item",
		Long: "Reads the server's local age key (.local/sops/servers/<server>/age-key.txt) " +
			"and stores it (leak-free, via a stdin template) in the 1Password item " +
			"op://<vault>/<server> as the `age-key` field, then verifies the read-back. " +
			"Removes the single-copy risk: the key survives a lost laptop.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server, ctx, err := serverArg(g, args)
			if err != nil {
				return err
			}
			if ctx.Vault == "" {
				return ExitError{Code: 64, Message: "no 1Password vault configured: set `vault = \"...\"` in portablevps.toml"}
			}
			keyPath := filepath.Join(ctx.RepoRoot, core.PerServerKeyRel(server))
			data, rerr := os.ReadFile(keyPath)
			if rerr != nil {
				return ExitError{Code: 66, Message: fmt.Sprintf("no local age key for %s at %s: %v", server, keyPath, rerr)}
			}
			ageKey := strings.TrimRight(string(data), "\n")
			if ageKey == "" {
				return ExitError{Code: 66, Message: fmt.Sprintf("local age key for %s is empty", server)}
			}

			created, err := escrowAgeKey(ctx, server, ageKey)
			if err != nil {
				return err
			}
			// Verify the escrowed value reads back exactly as the keystore will read it.
			ref := keystore.DefaultOpItem(ctx.Vault, server) + "/age-key"
			got, verr := opRead(ctx, ref)
			if verr != nil {
				return ExitError{Code: 70, Message: fmt.Sprintf("escrow verify (op read %s) failed: %v", ref, verr)}
			}
			if strings.TrimRight(got, "\n") != ageKey {
				return ExitError{Code: 70, Message: fmt.Sprintf("escrow verify mismatch for %s: read-back differs from the local key", server)}
			}
			action := "updated"
			if created {
				action = "created"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s (age-key escrowed and verified)\n", action, keystore.DefaultOpItem(ctx.Vault, server))
			return nil
		},
	}
	return cmd
}

func newKeyExportCmd(g *globalOptions) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "export [server]",
		Short: "Restore a server's age key from 1Password to the local file (break-glass)",
		Long: "Reads op://<vault>/<server>/age-key and writes it to the local file " +
			".local/sops/servers/<server>/age-key.txt (0600) — for a fresh laptop or " +
			"offline break-glass. Refuses to overwrite an existing file without --force.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server, ctx, err := serverArg(g, args)
			if err != nil {
				return err
			}
			if ctx.Vault == "" {
				return ExitError{Code: 64, Message: "no 1Password vault configured: set `vault = \"...\"` in portablevps.toml"}
			}
			ref := keystore.DefaultOpItem(ctx.Vault, server) + "/age-key"
			val, rerr := opRead(ctx, ref)
			if rerr != nil {
				return ExitError{Code: 70, Message: fmt.Sprintf("op read %s failed: %v", ref, rerr)}
			}
			val = strings.TrimRight(val, "\n")
			if val == "" {
				return ExitError{Code: 66, Message: fmt.Sprintf("%s is empty in 1Password", ref)}
			}
			keyPath := filepath.Join(ctx.RepoRoot, core.PerServerKeyRel(server))
			if fileExistsCli(keyPath) && !force {
				return ExitError{Code: 73, Message: fmt.Sprintf("%s already exists; pass --force to overwrite", keyPath)}
			}
			if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
				return ExitError{Code: 70, Message: err.Error()}
			}
			if err := os.WriteFile(keyPath, []byte(val+"\n"), 0o600); err != nil {
				return ExitError{Code: 70, Message: err.Error()}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s from %s\n", keyPath, ref)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing local key file")
	return cmd
}

// escrowAgeKey creates or updates the server's 1Password item with the age key,
// feeding the value through a stdin JSON template so it never appears in argv.
func escrowAgeKey(ctx *config.Context, server, ageKey string) (created bool, err error) {
	run := adapters.ExecRunner{}
	acc := ctx.OpAccount

	getArgs := []string{"item", "get", server, "--vault", ctx.Vault}
	if acc != "" {
		getArgs = append(getArgs, "--account", acc)
	}
	exists := false
	if _, gerr := run.Run("", "op", getArgs...); gerr == nil {
		exists = true
	}

	if exists {
		tmpl, _ := json.Marshal(map[string]any{"fields": []any{ageKeyField(ageKey)}})
		args := []string{"item", "edit", server, "--vault", ctx.Vault}
		if acc != "" {
			args = append(args, "--account", acc)
		}
		args = append(args, "-")
		if _, err := run.RunEnvInput("", nil, string(tmpl), "op", args...); err != nil {
			return false, ExitError{Code: 70, Message: fmt.Sprintf("op item edit %s: %v", server, err)}
		}
		return false, nil
	}

	tmpl, _ := json.Marshal(map[string]any{
		"title":    server,
		"category": "SECURE_NOTE",
		"fields":   []any{ageKeyField(ageKey)},
	})
	args := []string{"item", "create", "--vault", ctx.Vault}
	if acc != "" {
		args = append(args, "--account", acc)
	}
	args = append(args, "-")
	if _, err := run.RunEnvInput("", nil, string(tmpl), "op", args...); err != nil {
		return false, ExitError{Code: 70, Message: fmt.Sprintf("op item create %s: %v", server, err)}
	}
	return true, nil
}

// opRead reads a single op:// reference (used to verify escrow round-trips).
func opRead(ctx *config.Context, ref string) (string, error) {
	args := []string{"read"}
	if ctx.OpAccount != "" {
		args = append(args, "--account", ctx.OpAccount)
	}
	args = append(args, ref)
	return adapters.ExecRunner{}.Run("", "op", args...)
}
