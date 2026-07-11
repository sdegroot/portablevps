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
		Short: "Escrow per-server keys to/from 1Password (import/export)",
	}
	cmd.PersistentFlags().StringVar(&g.serverFlag, "server", "",
		"target server (default: default_server in portablevps.toml)")
	cmd.AddCommand(newKeyImportCmd(g), newKeyExportCmd(g))
	return cmd
}

// ageKeyField is the concealed field the keystore reads as op://<vault>/<server>/age-key.
func ageKeyField(ageKey string) map[string]any {
	return map[string]any{"id": "age-key", "label": "age-key", "type": "CONCEALED", "value": ageKey}
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
