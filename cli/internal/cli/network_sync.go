package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sdegroot/portablevps/internal/adapters"
	"github.com/sdegroot/portablevps/internal/config"
	"github.com/sdegroot/portablevps/internal/core"
	"github.com/sdegroot/portablevps/internal/netbird"
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
			"secrets on first creation), adds the running peer to those groups, and " +
			"removes it from any other managed group (one named by a policy, a server " +
			"or a user in the fleet config).",
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

			// Take away managed groups the server no longer declares, so a
			// role change also removes the old role's access.
			fleet, serverGroups, err := loadFleet(ctx)
			if err != nil {
				return err
			}
			managed := netbird.ManagedGroups(fleet, serverGroups)
			groups, err := client.ListGroups()
			if err != nil {
				return err
			}
			removed, err := client.RemovePeerFromUndeclared(peer.ID, groups, managed, srv.NetbirdGroups)
			if err != nil {
				return err
			}
			if len(removed) > 0 {
				fmt.Fprintf(out, "peer %s: removed from undeclared groups %v\n", srv.NetbirdName, removed)
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

// loadFleet returns the consumer's `.#netbird` intent plus every server's
// declared groups keyed by NetBird peer name.
func loadFleet(ctx *config.Context) (netbird.FleetPolicies, map[string][]string, error) {
	env := core.Env{RepoRoot: ctx.RepoRoot, Runner: adapters.ExecRunner{}, Getenv: os.Getenv}
	raw, err := core.LoadFleetNetbird(env)
	if err != nil {
		return netbird.FleetPolicies{}, nil, ExitError{Code: 70, Message: err.Error()}
	}
	var fleet netbird.FleetPolicies
	if err := json.Unmarshal(raw, &fleet); err != nil {
		return netbird.FleetPolicies{}, nil, ExitError{Code: 70, Message: fmt.Sprintf("parsing .#netbird: %v", err)}
	}
	servers, err := core.LoadServers(env)
	if err != nil {
		return netbird.FleetPolicies{}, nil, ExitError{Code: 70, Message: fmt.Sprintf("loading servers: %v", err)}
	}
	serverGroups := map[string][]string{}
	for _, srv := range servers {
		if len(srv.NetbirdGroups) > 0 {
			serverGroups[srv.NetbirdName] = srv.NetbirdGroups
		}
	}
	return fleet, serverGroups, nil
}

func newNetworkPolicySyncCmd(g *globalOptions) *cobra.Command {
	var token string
	var confirmDefaultDeny, dryRun bool
	cmd := &cobra.Command{
		Use:   "policy-sync",
		Short: "Reconcile the fleet's NetBird policies, group membership and users from .#netbird",
		Long: "Brings the NetBird account in line with the consumer flake:\n" +
			"  - creates/updates/prunes the CLI-managed access policies (portablevps:*),\n" +
			"  - sets each declared user's auto-groups (the `users` map) and strips\n" +
			"    managed groups from undeclared users,\n" +
			"  - makes every managed group contain exactly the declared servers' and\n" +
			"    users' peers (anything else is removed),\n" +
			"  - disables NetBird's Default allow-all when disableDefaultPolicy = true\n" +
			"    (needs --confirm-default-deny) and re-enables it when false.\n" +
			"Managed groups are the ones named by a policy, a server's netbird.groups or\n" +
			"a user; NetBird's All and console-made groups are never touched.\n" +
			"--dry-run only reads the account and prints the plan.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := config.Resolve(config.Flags{Project: g.project, Server: g.serverFlag}, os.Getenv)
			if err != nil {
				return err
			}
			fleet, serverGroups, err := loadFleet(ctx)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(fleet.Policies) == 0 && len(fleet.Users) == 0 && !fleet.DisableDefaultPolicy {
				fmt.Fprintln(out, "skip: no NetBird policies or users declared")
				return nil
			}
			if fleet.DisableDefaultPolicy && !confirmDefaultDeny && !dryRun {
				return ExitError{Code: 64, Message: "refusing to disable NetBird's default allow-all without --confirm-default-deny: " +
					"this makes the mesh default-deny; confirm operator SSH access is covered by a policy first (you administer servers over the mesh)"}
			}
			client, err := netbirdClient(ctx, token)
			if err != nil {
				return err
			}
			verb := func(action string) string {
				if dryRun {
					return "would " + action
				}
				return action
			}

			// --- read the account (the only thing --dry-run does) ---
			groups, err := client.ListGroups()
			if err != nil {
				return err
			}
			peers, err := client.ListPeers()
			if err != nil {
				return err
			}
			users, err := client.ListUsers()
			if err != nil {
				return err
			}
			policies, err := client.ListPolicies()
			if err != nil {
				return err
			}
			managed := netbird.ManagedGroups(fleet, serverGroups)
			plan := netbird.PlanMembership(netbird.DesiredMembership{
				Managed: managed, Servers: serverGroups, Users: fleet.Users,
			}, groups, peers, users)
			for _, w := range plan.Warnings {
				fmt.Fprintf(out, "warning: %s\n", w)
			}

			// --- groups ---
			groupIDs := map[string]string{}
			for _, grp := range groups {
				groupIDs[grp.Name] = grp.ID
			}
			for _, name := range plan.CreateGroups {
				fmt.Fprintf(out, "%s: NetBird group %s\n", verb("create"), name)
			}
			if !dryRun && len(plan.CreateGroups) > 0 {
				created, err := client.EnsureGroups(plan.CreateGroups, nil)
				if err != nil {
					return err
				}
				for n, id := range created {
					groupIDs[n] = id
				}
			}

			// --- policies ---
			existing := map[string]netbird.Policy{}
			for _, p := range policies {
				if strings.HasPrefix(p.Name, netbird.ManagedPolicyPrefix) {
					existing[p.Name] = p
				}
			}
			declared := map[string]bool{}
			for _, spec := range fleet.Policies {
				name := netbird.ManagedPolicyPrefix + spec.Name
				declared[name] = true
				p, exists := existing[name]
				switch {
				case exists && netbird.PolicyUpToDate(p, spec, groupIDs):
					fmt.Fprintf(out, "unchanged: policy %s\n", name)
				case dryRun && exists:
					fmt.Fprintf(out, "would update: policy %s\n", name)
				case dryRun:
					fmt.Fprintf(out, "would create: policy %s\n", name)
				default:
					if _, err := client.UpsertPolicy(spec, groupIDs, existing, func(action, name string) {
						fmt.Fprintf(out, "%s: policy %s\n", action, name)
					}); err != nil {
						return err
					}
				}
			}
			for _, name := range sortedNames(existing) {
				if declared[name] {
					continue
				}
				fmt.Fprintf(out, "%s: policy %s\n", verb("delete"), name)
				if !dryRun {
					if err := client.DeletePolicy(existing[name].ID); err != nil {
						return err
					}
				}
			}

			// --- users' auto-groups ---
			for _, uc := range plan.Users {
				fmt.Fprintf(out, "%s: user %s auto-groups %v -> %v\n", verb("update"), userLabel(uc.User), uc.Before, uc.After)
				if dryRun {
					continue
				}
				ids := make([]string, 0, len(uc.After))
				for _, n := range uc.After {
					if id, ok := groupIDs[n]; ok {
						ids = append(ids, id)
					} else {
						ids = append(ids, n) // an unmanaged id we could not name; keep it as-is
					}
				}
				if err := client.SetUserAutoGroups(uc.User, ids); err != nil {
					return err
				}
			}

			// --- managed group membership ---
			for _, gc := range plan.Groups {
				if len(gc.Add) > 0 {
					fmt.Fprintf(out, "%s: group %s add %v\n", verb("update"), gc.Group, gc.Add)
				}
				if len(gc.Remove) > 0 {
					fmt.Fprintf(out, "%s: group %s remove %v\n", verb("update"), gc.Group, gc.Remove)
				}
				if dryRun {
					continue
				}
				if err := client.SetGroupPeers(groupIDs[gc.Group], gc.Peers); err != nil {
					return err
				}
			}

			// --- NetBird's Default allow-all ---
			var def *netbird.Policy
			for i := range policies {
				if policies[i].Name == "Default" {
					def = &policies[i]
				}
			}
			switch {
			case def == nil:
				fmt.Fprintln(out, "warning: no NetBird 'Default' policy found")
			case fleet.DisableDefaultPolicy && def.Enabled:
				fmt.Fprintf(out, "%s: NetBird default allow-all — the mesh becomes default-deny\n", verb("disable"))
				if !dryRun {
					if err := client.SetPolicyEnabled(*def, false); err != nil {
						return err
					}
				}
			case !fleet.DisableDefaultPolicy && !def.Enabled:
				// The recovery path: flipping disableDefaultPolicy back to false
				// must actually restore allow-all, not just stop disabling it.
				fmt.Fprintf(out, "%s: NetBird default allow-all — the mesh becomes default-allow\n", verb("enable"))
				if !dryRun {
					if err := client.SetPolicyEnabled(*def, true); err != nil {
						return err
					}
				}
			case def.Enabled:
				fmt.Fprintln(out, "default allow-all: enabled (mesh is default-allow)")
			default:
				fmt.Fprintln(out, "default allow-all: disabled (mesh is default-deny)")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "NetBird API token (else [network].api_token)")
	cmd.Flags().BoolVar(&confirmDefaultDeny, "confirm-default-deny", false, "confirm disabling NetBird's default allow-all (mesh becomes default-deny)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "only read the account and print what would change")
	return cmd
}

func userLabel(u netbird.User) string {
	if u.Email != "" {
		return u.Email
	}
	return u.ID
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
