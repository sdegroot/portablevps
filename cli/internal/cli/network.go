package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/epistola-app/portablevps/internal/config"
	"github.com/epistola-app/portablevps/internal/netbird"
)

// newNetworkCmd is the mesh-exposure noun. Backend-neutral by intent; the
// NetBird backend is implemented today.
func newNetworkCmd(g *globalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "network",
		Short: "Manage mesh DNS and access (NetBird backend)",
	}
	cmd.PersistentFlags().StringVar(&g.serverFlag, "server", "",
		"target server (default: default_server in portablevps.toml)")
	cmd.AddCommand(
		newNetworkSyncCmd(g),
		newNetworkDNSSyncCmd(g),
		newNetworkPolicySyncCmd(g),
	)
	return cmd
}

// netbirdClient builds a NetBird API client from config (token resolved via the
// op:// resolver) with a --token override.
func netbirdClient(ctx *config.Context, tokenOverride string) (*netbird.Client, error) {
	token := tokenOverride
	if token == "" {
		token = resolveSecretRef(ctx, ctx.Network.APIToken)
	}
	if token == "" {
		return nil, ExitError{Code: 64, Message: "no NetBird API token: set [network].api_token in portablevps.toml or pass --token"}
	}
	// resolveSecretRef passes an unresolved reference through unchanged, which
	// would otherwise reach NetBird as a literal token and fail with a confusing
	// 401. Catch it here with an actionable message (op session expired, etc.).
	if isUnresolvedRef(token) {
		return nil, ExitError{Code: 66, Message: fmt.Sprintf("could not resolve the NetBird API token from %q "+
			"(is your 1Password session active? try `op signin`)", token)}
	}
	return netbird.New(token, ctx.Network.APIURL), nil
}

// isUnresolvedRef reports whether s is still a secret reference (op://, env://)
// that failed to resolve, rather than a real secret value.
func isUnresolvedRef(s string) bool {
	for _, scheme := range []string{"op://", "env://"} {
		if len(s) >= len(scheme) && s[:len(scheme)] == scheme {
			return true
		}
	}
	return false
}

func newNetworkDNSSyncCmd(g *globalOptions) *cobra.Command {
	var hf hostFlags
	var token, zone, zoneName string
	var groupIDs []string
	cmd := &cobra.Command{
		Use:   "dns-sync [server]",
		Short: "Sync a host's internal proxy DNS names into the NetBird DNS zone",
		Long: "Reads the host's generated proxy domain plan over SSH and upserts the " +
			"internal (mesh) DNS records into the NetBird custom zone.",
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
			z := zone
			if z == "" {
				z = ctx.DNSZone
			}
			if z == "" {
				return ExitError{Code: 64, Message: "no DNS zone: set dns_zone in portablevps.toml or pass --zone"}
			}
			client, err := netbirdClient(ctx, token)
			if err != nil {
				return err
			}

			ssh, cleanup, err := hf.sshAdapter(g, ctx, server)
			if err != nil {
				return err
			}
			defer cleanup()

			planJSON, err := ssh.Run(host, "portablevps-proxy-domain-plan")
			if err != nil {
				return ExitError{Code: 70, Message: fmt.Sprintf("reading proxy domain plan from %s: %v", host, err)}
			}
			records, err := netbird.RecordsFromPlan([]byte(planJSON), z)
			if err != nil {
				return ExitError{Code: 70, Message: err.Error()}
			}
			if len(records) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "no internal DNS records for zone %s in %s's plan\n", z, host)
				return nil
			}
			out := cmd.OutOrStdout()
			err = client.DNSSync(z, zoneName, groupIDs, records, func(action string, r netbird.Record) {
				fmt.Fprintf(out, "%s: %s %s %s ttl=%d\n", action, r.Name, r.Type, r.Content, r.TTL)
			})
			if err != nil {
				return err
			}
			return nil
		},
	}
	hf.register(cmd)
	cmd.Flags().StringVar(&token, "token", "", "NetBird API token (else [network].api_token)")
	cmd.Flags().StringVar(&zone, "zone", "", "DNS zone domain (else dns_zone)")
	cmd.Flags().StringVar(&zoneName, "zone-name", "", "DNS zone display name (default: the domain)")
	cmd.Flags().StringSliceVar(&groupIDs, "dns-group-ids", nil, "distribution group IDs (only needed to create a missing zone)")
	return cmd
}
