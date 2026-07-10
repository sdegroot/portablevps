package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/epistola-app/portablevps/internal/adapters"
	"github.com/epistola-app/portablevps/internal/config"
	"github.com/epistola-app/portablevps/internal/core"
	"github.com/epistola-app/portablevps/internal/netbird"
)

// managedSetupKeyName is the per-server reusable setup key this CLI owns.
func managedSetupKeyName(server string) string { return "portablevps-" + server }

// setupKeySopsIndex matches netbird.nix's setupKeySecret default ("netbird/setup-key").
const setupKeySopsIndex = `["netbird"]["setup-key"]`

// loadServer returns one logical server from the consumer flake (for its NetBird
// groups + peer name).
func loadServer(ctx *config.Context, server string) (core.Server, error) {
	servers, err := core.LoadServers(core.Env{RepoRoot: ctx.RepoRoot, Runner: adapters.ExecRunner{}, Getenv: os.Getenv})
	if err != nil {
		return core.Server{}, ExitError{Code: 70, Message: fmt.Sprintf("loading servers: %v", err)}
	}
	srv, ok := servers[server]
	if !ok {
		return core.Server{}, ExitError{Code: 64, Message: fmt.Sprintf("unknown server %q", server)}
	}
	return srv, nil
}

func newNetworkSyncCmd(g *globalOptions) *cobra.Command {
	var token string
	cmd := &cobra.Command{
		Use:   "sync [server]",
		Short: "Reconcile a server's NetBird groups and reusable setup key",
		Long: "Ensures the server's declared NetBird groups exist, ensures a reusable " +
			"setup key auto-joining those groups exists (stored into the server's sops " +
			"secrets on first creation), and adds the running peer to those groups.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server, ctx, err := serverArg(g, args)
			if err != nil {
				return err
			}
			srv, err := loadServer(ctx, server)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(srv.NetbirdGroups) == 0 {
				fmt.Fprintf(out, "skip: %s declares no netbird.groups\n", server)
				return nil
			}
			client, err := netbirdClient(ctx, token)
			if err != nil {
				return err
			}

			groupIDs, err := client.EnsureGroups(srv.NetbirdGroups, func(name string) {
				fmt.Fprintf(out, "create: NetBird group %s\n", name)
			})
			if err != nil {
				return err
			}
			ids := make([]string, 0, len(groupIDs))
			for _, id := range groupIDs {
				ids = append(ids, id)
			}

			keyName := managedSetupKeyName(server)
			plaintext, created, err := client.EnsureSetupKey(keyName, ids)
			if err != nil {
				return err
			}
			if created {
				if err := storeServerSecret(ctx, server, setupKeySopsIndex, plaintext); err != nil {
					return err
				}
				fmt.Fprintf(out, "created reusable setup key %s and stored it in secrets/%s.yaml\n", keyName, server)
			} else {
				fmt.Fprintf(out, "setup key %s already exists (auto-groups reconciled)\n", keyName)
			}

			peer, err := client.FindPeer(srv.NetbirdName)
			if err != nil {
				return err
			}
			if peer == nil {
				fmt.Fprintf(out, "peer: %s is not registered yet; it will be auto-grouped when it joins with the setup key\n", srv.NetbirdName)
				return nil
			}
			changed := []string{}
			for name, gid := range groupIDs {
				did, err := client.EnsurePeerInGroup(gid, peer.ID)
				if err != nil {
					return err
				}
				if did {
					changed = append(changed, name)
				}
			}
			if len(changed) > 0 {
				fmt.Fprintf(out, "peer %s: added to groups %v\n", srv.NetbirdName, changed)
			} else {
				fmt.Fprintf(out, "peer %s: already in all declared groups\n", srv.NetbirdName)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "NetBird API token (else [network].api_token)")
	return cmd
}

// storeServerSecret writes value into the server's sops file at index, feeding it
// through stdin (sops set --value-stdin) so it never appears in a process list.
func storeServerSecret(ctx *config.Context, server, index, value string) error {
	ageEnv, err := serverAgeEnv(ctx, server)
	if err != nil {
		return err
	}
	rel := filepath.Join("secrets", server+".yaml")
	if err := ensureEncryptedFile(ctx.RepoRoot, ageEnv, rel); err != nil {
		return err
	}
	if _, err := (adapters.ExecRunner{}).RunEnvInput(ctx.RepoRoot, ageEnv, toJSONString(value),
		"sops", "set", "--value-stdin", rel, index); err != nil {
		return ExitError{Code: 70, Message: fmt.Sprintf("storing secret in %s: %v", rel, err)}
	}
	return nil
}

func newNetworkPolicySyncCmd(g *globalOptions) *cobra.Command {
	var token string
	var confirmDefaultDeny bool
	cmd := &cobra.Command{
		Use:   "policy-sync",
		Short: "Reconcile the fleet's NetBird access policies from .#netbird",
		Long: "Creates/updates/prunes the CLI-managed access policies declared in the " +
			"consumer flake's .#netbird output, and optionally disables NetBird's " +
			"default allow-all (making the mesh default-deny).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := config.Resolve(config.Flags{Project: g.project, Server: g.serverFlag}, os.Getenv)
			if err != nil {
				return err
			}
			raw, err := core.LoadFleetNetbird(core.Env{RepoRoot: ctx.RepoRoot, Runner: adapters.ExecRunner{}, Getenv: os.Getenv})
			if err != nil {
				return ExitError{Code: 70, Message: err.Error()}
			}
			var fleet netbird.FleetPolicies
			if err := json.Unmarshal(raw, &fleet); err != nil {
				return ExitError{Code: 70, Message: fmt.Sprintf("parsing .#netbird: %v", err)}
			}
			out := cmd.OutOrStdout()
			if len(fleet.Policies) == 0 && !fleet.DisableDefaultPolicy {
				fmt.Fprintln(out, "skip: no NetBird policies declared")
				return nil
			}
			if fleet.DisableDefaultPolicy && !confirmDefaultDeny {
				return ExitError{Code: 64, Message: "refusing to disable NetBird's default allow-all without --confirm-default-deny: " +
					"this makes the mesh default-deny; confirm operator SSH access is covered by a policy first (you administer servers over the mesh)"}
			}
			client, err := netbirdClient(ctx, token)
			if err != nil {
				return err
			}

			// Resolve every group named by any declared policy.
			names := map[string]bool{}
			for _, p := range fleet.Policies {
				for _, s := range p.Sources {
					names[s] = true
				}
				for _, d := range p.Destinations {
					names[d] = true
				}
			}
			groupNames := make([]string, 0, len(names))
			for n := range names {
				groupNames = append(groupNames, n)
			}
			groupIDs := map[string]string{}
			if len(groupNames) > 0 {
				groupIDs, err = client.EnsureGroups(groupNames, func(name string) {
					fmt.Fprintf(out, "create: NetBird group %s\n", name)
				})
				if err != nil {
					return err
				}
			}

			// Index existing managed policies so we can update and prune them.
			all, err := client.ListPolicies()
			if err != nil {
				return err
			}
			existing := map[string]netbird.Policy{}
			for _, p := range all {
				if len(p.Name) >= len(netbird.ManagedPolicyPrefix) && p.Name[:len(netbird.ManagedPolicyPrefix)] == netbird.ManagedPolicyPrefix {
					existing[p.Name] = p
				}
			}
			declared := map[string]bool{}
			for _, spec := range fleet.Policies {
				name, err := client.UpsertPolicy(spec, groupIDs, existing, func(action, name string) {
					fmt.Fprintf(out, "%s: policy %s\n", action, name)
				})
				if err != nil {
					return err
				}
				declared[name] = true
			}
			for name, p := range existing {
				if !declared[name] {
					if err := client.DeletePolicy(p.ID); err != nil {
						return err
					}
					fmt.Fprintf(out, "delete: policy %s\n", name)
				}
			}

			if fleet.DisableDefaultPolicy {
				def, err := client.FindDefaultPolicy()
				if err != nil {
					return err
				}
				switch {
				case def == nil:
					fmt.Fprintln(out, "warning: no NetBird 'Default' policy found to disable")
				case def.Enabled:
					if err := client.SetPolicyEnabled(*def, false); err != nil {
						return err
					}
					fmt.Fprintln(out, "disabled: NetBird default allow-all — the mesh is now default-deny")
				default:
					fmt.Fprintln(out, "default allow-all already disabled")
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "NetBird API token (else [network].api_token)")
	cmd.Flags().BoolVar(&confirmDefaultDeny, "confirm-default-deny", false, "confirm disabling NetBird's default allow-all (mesh becomes default-deny)")
	return cmd
}
