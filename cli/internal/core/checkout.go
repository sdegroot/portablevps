package core

import (
	"fmt"
	"strconv"
	"strings"
)

// CheckoutState is how the consumer checkout a switch builds from relates to
// the remote's default branch. A switch deploys the LOCAL tree, so a checkout
// that is behind that branch silently rolls the host back to an older config —
// and an app with one-way migrations (authentik) then runs old code against a
// newer database.
type CheckoutState struct {
	GitRepo   bool
	Head      string   // short HEAD commit
	RemoteRef string   // e.g. "origin/main"; "" when none could be determined
	Behind    int      // commits on RemoteRef that HEAD lacks
	Ahead     int      // commits on HEAD that RemoteRef lacks
	Dirty     []string // tracked paths under the project with uncommitted changes
	FetchErr  error    // refreshing RemoteRef failed; counts use the last fetched copy
}

// CheckoutEnv is what InspectCheckout needs from the outside world.
type CheckoutEnv struct {
	RepoRoot string        // the consumer project (flake) directory
	Runner   CommandRunner // runs git
	// Fetch refreshes one branch of remote before comparing; nil skips the
	// refresh and compares against whatever was fetched last.
	Fetch  func(dir, remote, branch string) error
	Remote string // default "origin"
}

// InspectCheckout reports where the checkout at env.RepoRoot stands relative to
// the remote's default branch: <remote>/HEAD, else <remote>/main, else
// <remote>/master.
func InspectCheckout(env CheckoutEnv) CheckoutState {
	var s CheckoutState
	git := func(args ...string) (string, error) {
		out, err := env.Runner.Run(env.RepoRoot, "git", args...)
		return strings.TrimSpace(out), err
	}
	if out, err := git("rev-parse", "--is-inside-work-tree"); err != nil || out != "true" {
		return s
	}
	s.GitRepo = true
	s.Head, _ = git("rev-parse", "--short", "HEAD")

	remote := env.Remote
	if remote == "" {
		remote = "origin"
	}
	if ref, err := git("symbolic-ref", "-q", "refs/remotes/"+remote+"/HEAD"); err == nil && ref != "" {
		s.RemoteRef = strings.TrimPrefix(ref, "refs/remotes/")
	} else {
		for _, b := range []string{"main", "master"} {
			if _, err := git("rev-parse", "--verify", "-q", "refs/remotes/"+remote+"/"+b); err == nil {
				s.RemoteRef = remote + "/" + b
				break
			}
		}
	}

	if s.RemoteRef != "" {
		if env.Fetch != nil {
			s.FetchErr = env.Fetch(env.RepoRoot, remote, strings.TrimPrefix(s.RemoteRef, remote+"/"))
		}
		// "<behind>\t<ahead>": left = only on the remote ref, right = only on HEAD.
		if out, err := git("rev-list", "--left-right", "--count", s.RemoteRef+"...HEAD"); err == nil {
			if f := strings.Fields(out); len(f) == 2 {
				s.Behind, _ = strconv.Atoi(f[0])
				s.Ahead, _ = strconv.Atoi(f[1])
			}
		}
	}

	// Tracked files changed against HEAD, staged or not, under the project
	// directory only: the flake is evaluated from there, so an unrelated edit
	// elsewhere in a monorepo does not block a deploy. (Not `status
	// --porcelain`: the runner trims output, which would eat the leading
	// space of a " M path" line.)
	if out, err := git("diff", "--name-only", "HEAD", "--", "."); err == nil && out != "" {
		s.Dirty = strings.Split(out, "\n")
	}
	return s
}

// Assess splits the state into reasons to refuse a switch and lesser warnings.
func (s CheckoutState) Assess() (refuse, warn []string) {
	if !s.GitRepo {
		return nil, []string{"not a git checkout; cannot tell whether it matches the remote"}
	}
	if s.RemoteRef == "" {
		warn = append(warn, "no remote default branch found (origin/HEAD, origin/main, origin/master); cannot tell whether this checkout is current")
	}
	if s.FetchErr != nil {
		warn = append(warn, fmt.Sprintf("could not refresh %s (%v); compared with the last fetched copy", s.RemoteRef, s.FetchErr))
	}
	if s.Behind > 0 {
		refuse = append(refuse, fmt.Sprintf(
			"checkout %s is %d commit(s) behind %s: switching would roll the host back to an older config; update it (git pull, or a fresh worktree at %s)",
			s.Head, s.Behind, s.RemoteRef, s.RemoteRef))
	}
	if len(s.Dirty) > 0 {
		shown := s.Dirty
		more := ""
		if len(shown) > 5 {
			shown, more = shown[:5], fmt.Sprintf(" (+%d more)", len(s.Dirty)-5)
		}
		refuse = append(refuse, fmt.Sprintf(
			"uncommitted changes to tracked files would be deployed: %s%s; commit them first",
			strings.Join(shown, ", "), more))
	}
	if s.Ahead > 0 {
		warn = append(warn, fmt.Sprintf(
			"%d commit(s) not on %s: a host that self-upgrades from %s reverts them on its next poll; push first",
			s.Ahead, s.RemoteRef, s.RemoteRef))
	}
	return refuse, warn
}

// GuardCheckout inspects the checkout, reports what it finds as the
// "checkout" phase, and returns an error (exit 65) when the switch must not
// go ahead. allowStale (--allow-stale) and dryRun downgrade refusals to
// warnings: a dry run changes nothing, and an operator may deliberately deploy
// a branch.
func GuardCheckout(env CheckoutEnv, report func(phase, status, msg string), allowStale, dryRun bool) error {
	if report == nil {
		report = func(string, string, string) {}
	}
	report("checkout", "run", "checking the checkout against the remote's default branch")
	s := InspectCheckout(env)
	refuse, warn := s.Assess()
	for _, w := range warn {
		report("checkout", "warn", w)
	}
	if len(refuse) > 0 {
		if allowStale || dryRun {
			why := "--allow-stale"
			if !allowStale {
				why = "dry run"
			}
			for _, r := range refuse {
				report("checkout", "warn", r+" (allowed: "+why+")")
			}
			return nil
		}
		for _, r := range refuse {
			report("checkout", "fail", r)
		}
		return deployErr(65, "refusing to switch from this checkout: %s (pass --allow-stale to override)", strings.Join(refuse, "; "))
	}
	if s.GitRepo && s.RemoteRef != "" {
		report("checkout", "ok", fmt.Sprintf("%s contains %s", s.Head, s.RemoteRef))
	}
	return nil
}
