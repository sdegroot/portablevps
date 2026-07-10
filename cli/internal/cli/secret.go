package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/epistola-app/portablevps/internal/adapters"
	"github.com/epistola-app/portablevps/internal/config"
	"github.com/epistola-app/portablevps/internal/core"
)

// newSecretCmd is the sops/age ceremony noun. The CLI is the sole editor of
// .sops.yaml, so operators never hand-edit it or run `sops updatekeys`.
func newSecretCmd(g *globalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage sops/age secrets and recipients",
		Long:  "Owns the sops/age ceremony end-to-end: per-server keys, .sops.yaml recipients, and re-keying.",
	}
	cmd.PersistentFlags().StringVar(&g.serverFlag, "server", "",
		"target server (default: default_server in portablevps.toml)")
	cmd.AddCommand(
		newSecretKeygenCmd(g),
		newSecretInitCmd(g),
		newSecretSyncKeysCmd(g),
		newSecretSetCmd(g),
		newSecretEditCmd(g),
		newSecretShowCmd(g),
	)
	return cmd
}

func secretEnv(g *globalOptions) (core.SecretEnv, *config.Context, error) {
	ctx, err := config.Resolve(config.Flags{Project: g.project}, os.Getenv)
	if err != nil {
		return core.SecretEnv{}, nil, err
	}
	return core.SecretEnv{
		RepoRoot:       ctx.RepoRoot,
		Runner:         adapters.ExecRunner{},
		OperatorAgeKey: ctx.OperatorAgeKey,
	}, ctx, nil
}

// serverArg resolves the target server from --server, the positional arg, or
// the configured default.
func serverArg(g *globalOptions, args []string) (string, *config.Context, error) {
	ctx, err := config.Resolve(config.Flags{Project: g.project, Server: g.serverFlag}, os.Getenv)
	if err != nil {
		return "", nil, err
	}
	server := ctx.Server
	if len(args) > 0 {
		server = args[0]
	}
	if server == "" {
		return "", ctx, ExitError{Code: 64, Message: "no server given: pass a name, --server, or set default_server in portablevps.toml"}
	}
	return server, ctx, nil
}

func newSecretKeygenCmd(g *globalOptions) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Generate the operator age identity (.local/sops/age-key.txt + keys/operator-age.pub)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, ctx, err := secretEnv(g)
			if err != nil {
				return err
			}
			keyRel := ctx.OperatorAgeKey
			keyPath := filepath.Join(ctx.RepoRoot, keyRel)
			if fileExistsCli(keyPath) && !force {
				return ExitError{Code: 73, Message: fmt.Sprintf("%s already exists; pass --force to replace it", keyRel)}
			}
			if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
				return err
			}
			runner := adapters.ExecRunner{}
			if _, err := runner.Run("", "age-keygen", "-o", keyPath); err != nil {
				return ExitError{Code: 70, Message: fmt.Sprintf("age-keygen: %v", err)}
			}
			_ = os.Chmod(keyPath, 0o600)
			recipient, err := runner.Run("", "age-keygen", "-y", keyPath)
			if err != nil {
				return ExitError{Code: 70, Message: fmt.Sprintf("deriving recipient: %v", err)}
			}
			pubPath := filepath.Join(ctx.RepoRoot, "keys", "operator-age.pub")
			_ = os.MkdirAll(filepath.Dir(pubPath), 0o755)
			if err := os.WriteFile(pubPath, []byte(recipient+"\n"), 0o644); err != nil {
				return err
			}
			_ = env // reserved for future use
			fmt.Fprintf(cmd.OutOrStdout(), "operator age key: %s\nrecipient: %s\ncommitted: keys/operator-age.pub\n", keyRel, recipient)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing operator key")
	return cmd
}

func newSecretInitCmd(g *globalOptions) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init [server]",
		Short: "Create a server's age key and register it in .sops.yaml (owns the whole ceremony)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server, _, err := serverArg(g, args)
			if err != nil {
				return err
			}
			env, _, err := secretEnv(g)
			if err != nil {
				return err
			}
			res, err := core.SecretInit(env, server, force)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "created per-server age key: %s\n", res.KeyPath)
			fmt.Fprintf(out, "recipient: %s\n", res.Recipient)
			fmt.Fprintf(out, "registered in %s for %s\n", res.SopsConfig, res.SecretsFile)
			if res.Rekeyed {
				fmt.Fprintf(out, "re-encrypted existing %s to the new recipient set\n", res.SecretsFile)
			} else {
				fmt.Fprintf(out, "next: `portablevps secret set --server %s ...` (creates %s on first write)\n", server, res.SecretsFile)
			}
			warnIfDirty(cmd.ErrOrStderr(), env.RepoRoot)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing per-server key")
	return cmd
}

func newSecretSyncKeysCmd(g *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "sync-keys [server]",
		Short: "Re-encrypt a server's secrets to the recipients now in .sops.yaml (rotation / add operator)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server, _, err := serverArg(g, args)
			if err != nil {
				return err
			}
			env, _, err := secretEnv(g)
			if err != nil {
				return err
			}
			rel := filepath.Join("secrets", server+".yaml")
			if !fileExistsCli(filepath.Join(env.RepoRoot, rel)) {
				return ExitError{Code: 66, Message: fmt.Sprintf("%s does not exist", rel)}
			}
			if err := core.SyncKeysFile(env, rel); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "re-keyed %s\n", rel)
			return nil
		},
	}
}

func newSecretSetCmd(g *globalOptions) *cobra.Command {
	var key, value string
	var valueStdin bool
	cmd := &cobra.Command{
		Use:   "set [server]",
		Short: "Set one secret value non-interactively",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server, ctx, err := serverArg(g, args)
			if err != nil {
				return err
			}
			if key == "" {
				return ExitError{Code: 64, Message: "--key is required (e.g. --key '[\"netbird\"][\"setup-key\"]')"}
			}
			if valueStdin {
				b, _ := io.ReadAll(cmd.InOrStdin())
				value = string(b)
			}
			rel := filepath.Join("secrets", server+".yaml")
			if err := ensureEncryptedFile(ctx.RepoRoot, ctx.OperatorAgeKey, rel); err != nil {
				return err
			}
			// sops set expects a JSON-encoded value.
			jsonVal := toJSONString(value)
			_, err = adapters.ExecRunner{}.RunEnv(ctx.RepoRoot,
				map[string]string{"SOPS_AGE_KEY_FILE": ctx.OperatorAgeKey},
				"sops", "set", rel, key, jsonVal)
			if err != nil {
				return ExitError{Code: 70, Message: fmt.Sprintf("sops set: %v", err)}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "set %s in %s\n", key, rel)
			return nil
		},
	}
	cmd.Flags().StringVar(&key, "key", "", "sops key path, e.g. '[\"netbird\"][\"setup-key\"]'")
	cmd.Flags().StringVar(&value, "value", "", "the value to set")
	cmd.Flags().BoolVar(&valueStdin, "value-stdin", false, "read the value from stdin (CI-safe; no value in argv)")
	return cmd
}

func newSecretEditCmd(g *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "edit [server]",
		Short: "Edit a server's secrets in $EDITOR through sops (interactive only)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server, ctx, err := serverArg(g, args)
			if err != nil {
				return err
			}
			if !g.interactive() {
				return ExitError{Code: 64, Message: "secret edit is interactive; use `secret set` for non-interactive changes"}
			}
			rel := filepath.Join("secrets", server+".yaml")
			c := exec.Command("sops", rel)
			c.Dir = ctx.RepoRoot
			c.Env = append(os.Environ(), "SOPS_AGE_KEY_FILE="+ctx.OperatorAgeKey)
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := c.Run(); err != nil {
				return ExitError{Code: 70, Message: fmt.Sprintf("sops edit: %v", err)}
			}
			return nil
		},
	}
}

func newSecretShowCmd(g *globalOptions) *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "show [server]",
		Short: "Print a server's decrypted secrets (--check verifies without printing)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server, ctx, err := serverArg(g, args)
			if err != nil {
				return err
			}
			rel := filepath.Join("secrets", server+".yaml")
			out, err := adapters.ExecRunner{}.RunEnv(ctx.RepoRoot,
				map[string]string{"SOPS_AGE_KEY_FILE": ctx.OperatorAgeKey},
				"sops", "-d", rel)
			if err != nil {
				return ExitError{Code: 70, Message: fmt.Sprintf("sops -d: %v", err)}
			}
			if check {
				fmt.Fprintf(cmd.OutOrStdout(), "%s decrypts and is non-empty (%d bytes)\n", rel, len(out))
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), out)
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "verify the file decrypts without printing secret values")
	return cmd
}
