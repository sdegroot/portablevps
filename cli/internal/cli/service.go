package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/sdegroot/portablevps/internal/adapters"
	"github.com/sdegroot/portablevps/internal/config"
	"github.com/sdegroot/portablevps/internal/core"
	"github.com/sdegroot/portablevps/internal/netbird"
	"github.com/sdegroot/portablevps/internal/output"
)

// newServiceCmd is the service-identity noun: operations that move a service
// (and its service-keyed backup repo) across machines.
func newServiceCmd(g *globalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Move and restore services across machines (data movement / DR)",
	}
	cmd.PersistentFlags().StringVar(&g.serverFlag, "server", "",
		"the service's server config (default: default_server in portablevps.toml)")
	cmd.AddCommand(newServiceMigrateCmd(g), newServiceRestoreCmd(g))
	return cmd
}

func newServiceMigrateCmd(g *globalOptions) *cobra.Command {
	var sourceServer, sourceHost, targetHost, marker string
	var prewarmTLS bool
	var hf hostFlags
	cmd := &cobra.Command{
		Use:   "migrate [server]",
		Short: "Move a service from one running host to another via its backup repo",
		Long: "Puts a warm target in restore mode, verifies its certificates before " +
			"the source is touched, quiesces source writers while PostgreSQL remains " +
			"online for the final backup, restores onto the target, switches it to " +
			"normal, and verifies. Repoints internal mesh DNS automatically when a " +
			"NetBird API token and DNS zone are configured; otherwise `network " +
			"dns-sync` is a manual follow-up.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server, ctx, err := serverArg(g, args)
			if err != nil {
				return err
			}
			if sourceHost == "" || targetHost == "" {
				return ExitError{Code: 64, Message: "migrate needs --source-host and --target-host"}
			}
			if sourceServer == "" {
				return ExitError{Code: 64, Message: "migrate needs --source-server so both service definitions can be validated"}
			}
			servers, err := core.LoadServers(core.Env{RepoRoot: ctx.RepoRoot, Runner: adapters.ExecRunner{}, Getenv: os.Getenv})
			if err != nil {
				return ExitError{Code: 70, Message: fmt.Sprintf("loading server registry: %v", err)}
			}
			source, ok := servers[sourceServer]
			if !ok {
				return ExitError{Code: 64, Message: "unknown source server: " + sourceServer}
			}
			target, ok := servers[server]
			if !ok {
				return ExitError{Code: 64, Message: "unknown target server: " + server}
			}
			if source.BackupRepository == "" || target.BackupRepository == "" || source.BackupRepository != target.BackupRepository {
				return ExitError{Code: 64, Message: "source and target must declare the same non-empty backupRepository"}
			}
			if source.ServiceKey == "" || target.ServiceKey == "" || source.ServiceKey != target.ServiceKey {
				return ExitError{Code: 64, Message: "source and target must declare the same non-empty serviceKey"}
			}
			ssh, cleanup, err := hf.sshAdapter(g, ctx, server)
			if err != nil {
				return err
			}
			defer cleanup()

			prog := output.NewProgress(cmd.OutOrStdout(), "service.migrate", server, g.json)
			var certs core.CertPrewarmer
			if prewarmTLS {
				certs = adapters.CurlCertChecker{}
			}
			env := core.ServiceEnv{
				RepoRoot: ctx.RepoRoot,
				Host:     ssh,
				Stream:   adapters.ExecRunner{},
				Report:   func(phase, status, msg string) { prog.Phase(phase, status, msg) },
				Certs:    certs,
				SyncDNS:  migrateDNSSyncFunc(ctx, ssh, cmd),
			}
			if err := core.Migrate(env, core.MigrateOpts{
				Server: server, SourceHost: sourceHost, TargetHost: targetHost, Marker: marker,
			}); err != nil {
				prog.Result("fail", err.Error(), exitCodeOf(err))
				return silentExit(err)
			}
			prog.Result("pass", server+" migrated "+sourceHost+" -> "+targetHost, 0, "target", targetHost)
			return nil
		},
	}
	cmd.Flags().StringVar(&sourceHost, "source-host", "", "the current (source) host address")
	cmd.Flags().StringVar(&sourceServer, "source-server", "", "the current source server definition (required for repository validation)")
	cmd.Flags().StringVar(&targetHost, "target-host", "", "the destination (target) host address")
	cmd.Flags().StringVar(&marker, "marker", "", "optional demo marker to verify before/after")
	cmd.Flags().BoolVar(&prewarmTLS, "prewarm-tls", true, "abort before touching the source if the target's certificate never becomes valid")
	cmd.Flags().StringVar(&hf.sshPort, "ssh-port", "22", "admin SSH port")
	cmd.Flags().StringVar(&hf.adminKey, "admin-key", "", "admin SSH private key override (default: 1Password/per-server file/cloud-admin fallback)")
	return cmd
}

// migrateDNSSyncFunc builds Migrate's optional automatic DNS-repoint hook.
// Mirrors newNetworkDNSSyncCmd's zone selection and sync call, but — like the
// legacy Python migrate-service command it replaces — silently declines (nil)
// rather than erroring when no NetBird API token or DNS zone is configured;
// automatic repointing is a bonus on top of a successful migration, not a
// precondition for one. Once a sync is actually attempted, real failures
// (bad token, zone errors, ...) still surface as errors from the returned
// function.
func migrateDNSSyncFunc(ctx *config.Context, ssh core.HostRunner, cmd *cobra.Command) func(host string) error {
	token := resolveSecretRef(ctx, ctx.Network.APIToken)
	if token == "" || isUnresolvedRef(token) {
		return nil
	}
	var zones []string
	switch {
	case len(ctx.Network.ManagedDNSZones) > 0:
		zones = ctx.Network.ManagedDNSZones
	case ctx.DNSZone != "":
		zones = []string{ctx.DNSZone}
	default:
		return nil
	}
	client := netbird.New(token, ctx.Network.APIURL)
	out := cmd.OutOrStdout()
	return func(host string) error {
		planJSON, err := ssh.Run(host, "portablevps-proxy-domain-plan")
		if err != nil {
			return fmt.Errorf("reading proxy domain plan: %w", err)
		}
		byZone, _, err := netbird.RecordsFromPlanByZone([]byte(planJSON), zones)
		if err != nil {
			return err
		}
		ownedTarget, err := netbird.PlanTarget([]byte(planJSON))
		if err != nil {
			return err
		}
		for _, z := range zones {
			records := byZone[z]
			if len(records) == 0 {
				continue
			}
			if err := client.DNSSync(z, "", nil, records, ownedTarget, true, func(action string, r netbird.Record) {
				fmt.Fprintf(out, "%s [%s]: %s %s %s ttl=%d\n", action, z, r.Name, r.Type, r.Content, r.TTL)
			}); err != nil {
				return err
			}
		}
		return nil
	}
}

func newServiceRestoreCmd(g *globalOptions) *cobra.Command {
	var hf hostFlags
	var marker string
	cmd := &cobra.Command{
		Use:   "restore [server]",
		Short: "Restore a service onto an already-installed host from its backup (DR)",
		Long: "Puts the host in restore mode, runs restore.sh, switches to normal, " +
			"starts apps, and verifies — for disaster recovery onto a fresh box.",
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
			ssh, cleanup, err := hf.sshAdapter(g, ctx, server)
			if err != nil {
				return err
			}
			defer cleanup()

			prog := output.NewProgress(cmd.OutOrStdout(), "service.restore", server, g.json)
			env := core.ServiceEnv{
				RepoRoot: ctx.RepoRoot,
				Host:     ssh,
				Stream:   adapters.ExecRunner{},
				Report:   func(phase, status, msg string) { prog.Phase(phase, status, msg) },
			}
			if err := core.Restore(env, core.RestoreOpts{Server: server, Host: host, Marker: marker}); err != nil {
				prog.Result("fail", err.Error(), exitCodeOf(err))
				return silentExit(err)
			}
			prog.Result("pass", host+" restored .#"+server, 0, "host", host)
			return nil
		},
	}
	hf.register(cmd)
	cmd.Flags().StringVar(&marker, "marker", "", "optional demo marker to verify after restore")
	return cmd
}
