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
	"github.com/epistola-app/portablevps/internal/keystore"
)

// newSecretCmd is the sops/age ceremony noun. The CLI is the sole editor of
// .sops.yaml, so operators never hand-edit it. Model A: each server's secrets
// are encrypted to that server's own age key only (no operator master key);
// operations resolve that key through the keystore (1Password -> file fallback).
func newSecretCmd(g *globalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage sops/age secrets and per-server recipients",
	}
	cmd.PersistentFlags().StringVar(&g.serverFlag, "server", "",
		"target server (default: default_server in portablevps.toml)")
	cmd.AddCommand(
		newSecretInitCmd(g),
		newSecretSyncKeysCmd(g),
		newSecretSetCmd(g),
		newSecretEditCmd(g),
		newSecretShowCmd(g),
	)
	return cmd
}

func secretEnv(g *globalOptions) (core.SecretEnv, *config.Context, error) {
	ctx, err := config.Resolve(config.Flags{Project: g.project, Server: g.serverFlag}, os.Getenv)
	if err != nil {
		return core.SecretEnv{}, nil, err
	}
	return core.SecretEnv{RepoRoot: ctx.RepoRoot, Runner: adapters.ExecRunner{}}, ctx, nil
}

// serverArg resolves the target server. Precedence: positional arg > --server /
// env / default_server. If none is given and the consumer directory defines
// exactly ONE server, that server is used automatically — so a single-server
// directory needs no --server and no default_server (portablevps's simplest,
// primary mode: one directory, one server).
func serverArg(g *globalOptions, args []string) (string, *config.Context, error) {
	ctx, err := config.Resolve(config.Flags{Project: g.project, Server: g.serverFlag}, os.Getenv)
	if err != nil {
		return "", nil, err
	}
	if len(args) > 0 {
		return args[0], ctx, nil
	}
	if ctx.Server != "" {
		return ctx.Server, ctx, nil
	}
	// No explicit server: auto-select when the flake defines exactly one.
	servers, lerr := core.LoadServers(core.Env{RepoRoot: ctx.RepoRoot, Runner: adapters.ExecRunner{}, Getenv: os.Getenv})
	if lerr == nil {
		switch len(servers) {
		case 1:
			for name := range servers {
				return name, ctx, nil
			}
		case 0:
			return "", ctx, ExitError{Code: 64, Message: "this directory defines no servers"}
		default:
			return "", ctx, ExitError{Code: 64, Message: "multiple servers defined: pass a name, --server, or set default_server in portablevps.toml"}
		}
	}
	return "", ctx, ExitError{Code: 64, Message: "no server given: pass a name, --server, or set default_server in portablevps.toml"}
}

// serverAgeEnv resolves the sops decryption env for a server: SOPS_AGE_KEY from
// the server's own age key, sourced 1Password -> file fallback.
func serverAgeEnv(ctx *config.Context, server string) (map[string]string, error) {
	store := keystore.Store{Runner: adapters.ExecRunner{}, OpAccount: ctx.OpAccount}
	ref := keystore.Ref{
		OpItem:   keystore.DefaultOpItem(ctx.Vault, server),
		FilePath: filepath.Join(ctx.RepoRoot, core.PerServerKeyRel(server)),
	}
	material, err := store.AgeMaterial(ref)
	if err != nil {
		return nil, ExitError{Code: 66, Message: fmt.Sprintf("cannot resolve the age key for %s (1Password/file): %v", server, err)}
	}
	return map[string]string{"SOPS_AGE_KEY": material}, nil
}

func newSecretInitCmd(g *globalOptions) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init [server]",
		Short: "Create a server's age key and register it as the sole recipient in .sops.yaml",
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
			fmt.Fprintf(out, "recipient (sole): %s\n", res.Recipient)
			fmt.Fprintf(out, "registered in %s for %s\n", res.SopsConfig, res.SecretsFile)
			fmt.Fprintf(out, "next: `portablevps secret set --server %s ...` (creates %s on first write)\n", server, res.SecretsFile)
			warnIfDirty(cmd.ErrOrStderr(), env.RepoRoot)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing per-server key (existing secrets encrypted to the old key become unreadable)")
	return cmd
}

func newSecretSyncKeysCmd(g *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "sync-keys [server]",
		Short: "Re-encrypt a server's secrets to the recipients now in .sops.yaml",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server, ctx, err := serverArg(g, args)
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
			ageEnv, err := serverAgeEnv(ctx, server)
			if err != nil {
				return err
			}
			if err := core.SyncKeysFile(env, rel, ageEnv); err != nil {
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
				return ExitError{Code: 64, Message: "--key is required (e.g. --key '[\"postgres\"][\"password\"]')"}
			}
			if valueStdin {
				b, _ := io.ReadAll(cmd.InOrStdin())
				value = string(b)
			}
			ageEnv, err := serverAgeEnv(ctx, server)
			if err != nil {
				return err
			}
			rel := filepath.Join("secrets", server+".yaml")
			if err := ensureEncryptedFile(ctx.RepoRoot, ageEnv, rel); err != nil {
				return err
			}
			_, err = adapters.ExecRunner{}.RunEnv(ctx.RepoRoot, ageEnv,
				"sops", "set", rel, key, toJSONString(value))
			if err != nil {
				return ExitError{Code: 70, Message: fmt.Sprintf("sops set: %v", err)}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "set %s in %s\n", key, rel)
			return nil
		},
	}
	cmd.Flags().StringVar(&key, "key", "", "sops key path, e.g. '[\"postgres\"][\"password\"]'")
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
			ageEnv, err := serverAgeEnv(ctx, server)
			if err != nil {
				return err
			}
			rel := filepath.Join("secrets", server+".yaml")
			c := exec.Command("sops", rel)
			c.Dir = ctx.RepoRoot
			c.Env = os.Environ()
			for k, v := range ageEnv {
				c.Env = append(c.Env, k+"="+v)
			}
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
			ageEnv, err := serverAgeEnv(ctx, server)
			if err != nil {
				return err
			}
			rel := filepath.Join("secrets", server+".yaml")
			out, err := adapters.ExecRunner{}.RunEnv(ctx.RepoRoot, ageEnv, "sops", "-d", rel)
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
