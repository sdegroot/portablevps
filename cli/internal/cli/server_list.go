package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/sdegroot/portablevps/internal/adapters"
	"github.com/sdegroot/portablevps/internal/config"
	"github.com/sdegroot/portablevps/internal/core"
)

// newServerListCmd lists every server the consumer flake defines with its
// public addresses and purpose — answering "what servers do we have, what are
// they for, and which one is this IP?" without SSH. Reads only the flake's
// serverInfo (one nix eval).
func newServerListCmd(g *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all servers, their public addresses and purpose",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := config.Resolve(config.Flags{Project: g.project, Server: g.serverFlag}, os.Getenv)
			if err != nil {
				return err
			}
			servers, err := core.LoadServers(core.Env{RepoRoot: ctx.RepoRoot, Runner: adapters.ExecRunner{}, Getenv: os.Getenv})
			if err != nil {
				return ExitError{Code: 70, Message: fmt.Sprintf("loading servers: %v", err)}
			}
			names := core.SortedNames(servers)

			if g.json {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				out := make([]map[string]string, 0, len(names))
				for _, n := range names {
					s := servers[n]
					out = append(out, map[string]string{
						"name":       n,
						"provider":   s.Provider,
						"publicIPv4": s.PublicIPv4,
						"publicIPv6": s.PublicIPv6,
						"purpose":    s.Purpose,
					})
				}
				return enc.Encode(out)
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tPROVIDER\tPUBLIC IPV4\tPUBLIC IPV6\tPURPOSE")
			for _, n := range names {
				s := servers[n]
				purpose := s.Purpose
				if purpose == "" {
					purpose = "(no purpose set)"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", n, s.Provider, orDash(s.PublicIPv4), orDash(s.PublicIPv6), purpose)
			}
			return w.Flush()
		},
	}
}

// orDash renders an unset optional column as "-" so the table stays aligned
// and an unrecorded address reads as unrecorded, not as a blank.
func orDash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}
