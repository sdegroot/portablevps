package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sdegroot/portablevps/internal/config"
	"github.com/sdegroot/portablevps/internal/core"
	"github.com/sdegroot/portablevps/internal/scaffold"
)

func newInitCmd(g *globalOptions) *cobra.Command {
	var server, flakeURL string
	cmd := &cobra.Command{
		Use:   "init [directory]",
		Short: "Scaffold a new consumer repository",
		Long: "Writes a consumer repository skeleton into directory (default: --project, " +
			"$PORTABLEVPS_PROJECT or the current directory): flake.nix, " +
			"servers/<server>.nix, portablevps.toml, mise.toml, .sops.yaml, " +
			".gitignore and README.md.\n\n" +
			"Existing files are never overwritten; an existing .gitignore only gets " +
			"the .local/ rules appended when missing. The directory may be a " +
			"subdirectory of an existing git repository (e.g. deploy/ in an app " +
			"repo). Keys and secrets are created afterwards with `pvps key generate` " +
			"and `pvps secret init`.\n\n" +
			"A release build pins the flake input and mise.toml to its own version.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := g.project
			if len(args) == 1 {
				dir = args[0]
			}
			root, err := config.ResolveRepoRoot(dir)
			if err != nil {
				return err
			}
			if server == "" {
				server = "example"
			}
			data := scaffold.DataFor(server, core.Version)
			if flakeURL != "" {
				data.FlakeURL = flakeURL
			}
			files, err := scaffold.Render(data)
			if err != nil {
				return ExitError{Code: 64, Message: err.Error()}
			}
			if info, statErr := os.Stat(root); statErr == nil && !info.IsDir() {
				return ExitError{Code: 64, Message: root + " exists and is not a directory"}
			}
			if existing, _ := filepath.Glob(filepath.Join(root, "servers", "*.nix")); len(existing) > 0 &&
				!fileExistsCli(filepath.Join(root, "servers", server+".nix")) {
				// Adding a second server is a copy of an existing file, not an init.
				return ExitError{Code: 64, Message: fmt.Sprintf(
					"%s already defines servers; copy one of them to servers/%s.nix instead", root, server)}
			}
			results, err := scaffold.Apply(root, files)
			if err != nil {
				return ExitError{Code: 70, Message: err.Error()}
			}

			out := cmd.OutOrStdout()
			if g.json {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{
					"root":     root,
					"server":   server,
					"flakeUrl": data.FlakeURL,
					"files":    results,
				})
			}
			for _, r := range results {
				fmt.Fprintf(out, "%-8s %s\n", r.Action, r.Path)
			}
			rel := root
			if wd, err := os.Getwd(); err == nil {
				if r, err := filepath.Rel(wd, root); err == nil && !strings.HasPrefix(r, "..") {
					rel = r
				}
			}
			project := ""
			if rel != "." {
				project = " --project " + shellQuote(rel)
			}
			fmt.Fprintf(out, "\nInitialised a portablevps consumer in %s. Next:\n", root)
			fmt.Fprintf(out, "  1. edit servers/%s.nix (provider, backup repository, app, domain)\n", server)
			fmt.Fprintf(out, "  2. git add the new files (Nix flakes only see tracked files)\n")
			fmt.Fprintf(out, "  3. pvps%s key generate %s\n", project, server)
			fmt.Fprintf(out, "  4. pvps%s secret init %s && pvps%s secret edit %s\n", project, server, project, server)
			fmt.Fprintf(out, "  5. pvps%s doctor --server %s\n", project, server)
			return nil
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "name of the first server (servers/<name>.nix; default: example)")
	cmd.Flags().StringVar(&flakeURL, "flake-url", "",
		"portablevps flake input URL (default: "+scaffold.DefaultFlakeURL+", pinned to the CLI's release tag)")
	return cmd
}

// shellQuote single-quotes s when it holds characters a shell would split or
// expand, so the printed next-step commands can be pasted as they are.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
