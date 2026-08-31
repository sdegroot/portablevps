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
// purpose — answering "what servers do we have and what are they for?" without
// SSH. Reads only the flake's serverInfo (one nix eval).
func newServerListCmd(g *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all servers and their purpose",
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
					out = append(out, map[string]string{"name": n, "provider": s.Provider, "purpose": s.Purpose})
				}
				return enc.Encode(out)
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tPROVIDER\tPURPOSE")
			for _, n := range names {
				s := servers[n]
				purpose := s.Purpose
				if purpose == "" {
					purpose = "(no purpose set)"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", n, s.Provider, purpose)
			}
			return w.Flush()
		},
	}
}
