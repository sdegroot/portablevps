package cli

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/sdegroot/portablevps/internal/adapters"
	"github.com/sdegroot/portablevps/internal/config"
	"github.com/sdegroot/portablevps/internal/core"
	"github.com/sdegroot/portablevps/internal/output"
)

// checkoutFetchTimeout bounds the refresh of the remote's default branch, so an
// unreachable remote delays a switch by at most this much (and then only warns).
const checkoutFetchTimeout = 30 * time.Second

// guardCheckout refuses to switch a host from a checkout that lacks commits on
// the remote's default branch, or has uncommitted changes in the project: a
// switch deploys the local tree, so either silently puts a different config on
// the host than the one everyone else sees. See core.GuardCheckout.
func guardCheckout(ctx *config.Context, prog *output.Progress, allowStale, dryRun bool) error {
	env := core.CheckoutEnv{
		RepoRoot: ctx.RepoRoot,
		Runner:   adapters.ExecRunner{},
		Fetch: func(dir, remote, branch string) error {
			return adapters.GitFetch(dir, remote, branch, checkoutFetchTimeout)
		},
	}
	return core.GuardCheckout(env, func(phase, status, msg string) { prog.Phase(phase, status, msg) }, allowStale, dryRun)
}

// registerAllowStale adds --allow-stale to a command that switches a host.
func registerAllowStale(cmd *cobra.Command, allowStale *bool) {
	cmd.Flags().BoolVar(allowStale, "allow-stale", false,
		"switch even if the checkout is behind the remote's default branch or has uncommitted changes in the project")
}
