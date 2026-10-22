package core

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRunner runs real git with the user's global/system config out of the way
// (signing, hooks, default branch), so the fixtures behave the same anywhere.
type gitRunner struct{}

func (gitRunner) Run(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitRunner{}.Run(dir, "git", args...)
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return out
}

func realFetch(dir, remote, branch string) error {
	_, err := gitRunner{}.Run(dir, "git", "fetch", "--quiet", remote, branch)
	return err
}

// fixture: a bare "remote" with one commit on main, a pusher clone that adds
// commits to it, and the operator's clone whose project lives in a
// subdirectory (the monorepo layout: <repo>/epistola is the flake).
type fixture struct {
	remote, pusher, clone, project string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	f := fixture{
		remote: filepath.Join(root, "remote.git"),
		pusher: filepath.Join(root, "pusher"),
		clone:  filepath.Join(root, "clone"),
	}
	mustGit(t, root, "init", "-q", "--bare", "-b", "main", f.remote)
	mustGit(t, root, "clone", "-q", f.remote, f.pusher)
	write(t, filepath.Join(f.pusher, "project", "flake.nix"), "{ }\n")
	write(t, filepath.Join(f.pusher, "README.md"), "readme\n")
	mustGit(t, f.pusher, "add", ".")
	mustGit(t, f.pusher, "commit", "-q", "-m", "init")
	mustGit(t, f.pusher, "push", "-q", "origin", "main")
	mustGit(t, root, "clone", "-q", f.remote, f.clone)
	f.project = filepath.Join(f.clone, "project")
	return f
}

func (f fixture) pushRemoteCommit(t *testing.T) {
	t.Helper()
	write(t, filepath.Join(f.pusher, "project", "new.nix"), "{ }\n")
	mustGit(t, f.pusher, "add", ".")
	mustGit(t, f.pusher, "commit", "-q", "-m", "newer config")
	mustGit(t, f.pusher, "push", "-q", "origin", "main")
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) env() CheckoutEnv {
	return CheckoutEnv{RepoRoot: f.project, Runner: gitRunner{}, Fetch: realFetch}
}

type phaseLog []string

func (p *phaseLog) report(phase, status, msg string) { *p = append(*p, status+": "+msg) }
func (p phaseLog) has(status, substr string) bool {
	for _, l := range p {
		if strings.HasPrefix(l, status+": ") && strings.Contains(l, substr) {
			return true
		}
	}
	return false
}

func TestCheckoutCurrent(t *testing.T) {
	f := newFixture(t)
	s := InspectCheckout(f.env())
	if !s.GitRepo || s.RemoteRef != "origin/main" || s.Behind != 0 || s.Ahead != 0 || len(s.Dirty) != 0 || s.FetchErr != nil {
		t.Fatalf("unexpected state: %+v", s)
	}
	var log phaseLog
	if err := GuardCheckout(f.env(), log.report, false, false); err != nil {
		t.Fatalf("current checkout refused: %v", err)
	}
	if !log.has("ok", "contains origin/main") {
		t.Fatalf("no ok line: %v", log)
	}
}

// The 2026-10-22 incident: the operator's checkout had not been pulled since
// main moved on. The fetch must reveal that even though the local
// origin/main was stale too.
func TestCheckoutBehindIsRefusedAfterFetch(t *testing.T) {
	f := newFixture(t)
	f.pushRemoteCommit(t)

	noFetch := f.env()
	noFetch.Fetch = nil
	if s := InspectCheckout(noFetch); s.Behind != 0 {
		t.Fatalf("without a fetch the stale origin/main should hide it, got behind=%d", s.Behind)
	}

	var log phaseLog
	err := GuardCheckout(f.env(), log.report, false, false)
	var de *DeployError
	if !errors.As(err, &de) || de.Code != 65 {
		t.Fatalf("want exit 65 refusal, got %v", err)
	}
	if !log.has("fail", "1 commit(s) behind origin/main") {
		t.Fatalf("refusal not reported: %v", log)
	}
}

func TestCheckoutBehindAllowedOrDryRun(t *testing.T) {
	f := newFixture(t)
	f.pushRemoteCommit(t)
	for _, tc := range []struct {
		name              string
		allowStale, dryRn bool
		why               string
	}{
		{"allow-stale", true, false, "--allow-stale"},
		{"dry-run", false, true, "dry run"},
	} {
		var log phaseLog
		if err := GuardCheckout(f.env(), log.report, tc.allowStale, tc.dryRn); err != nil {
			t.Fatalf("%s: refused: %v", tc.name, err)
		}
		if !log.has("warn", "behind origin/main") || !log.has("warn", "(allowed: "+tc.why+")") {
			t.Fatalf("%s: no allowed-warning: %v", tc.name, log)
		}
	}
}

func TestCheckoutDirtyOnlyCountsTheProject(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.clone, "README.md"), "edited outside the project\n")
	if s := InspectCheckout(f.env()); len(s.Dirty) != 0 {
		t.Fatalf("an edit outside the project dir must not count: %v", s.Dirty)
	}

	write(t, filepath.Join(f.project, "flake.nix"), "{ edited = true; }\n")
	s := InspectCheckout(f.env())
	if len(s.Dirty) != 1 || s.Dirty[0] != "project/flake.nix" {
		t.Fatalf("want project/flake.nix dirty, got %v", s.Dirty)
	}
	if err := GuardCheckout(f.env(), nil, false, false); err == nil {
		t.Fatal("dirty project deployed without --allow-stale")
	}

	// Staged changes count too.
	mustGit(t, f.clone, "add", "project/flake.nix")
	if s := InspectCheckout(f.env()); len(s.Dirty) != 1 {
		t.Fatalf("staged change not seen: %v", s.Dirty)
	}
}

func TestCheckoutUntrackedFilesDoNotCount(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.project, ".local", "secret"), "x\n")
	if s := InspectCheckout(f.env()); len(s.Dirty) != 0 {
		t.Fatalf("untracked files must not count (the flake does not see them): %v", s.Dirty)
	}
}

func TestCheckoutAheadOnlyWarns(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.project, "local.nix"), "{ }\n")
	mustGit(t, f.clone, "add", ".")
	mustGit(t, f.clone, "commit", "-q", "-m", "unpushed")
	var log phaseLog
	if err := GuardCheckout(f.env(), log.report, false, false); err != nil {
		t.Fatalf("ahead-only refused: %v", err)
	}
	if !log.has("warn", "1 commit(s) not on origin/main") {
		t.Fatalf("no ahead warning: %v", log)
	}
}

func TestCheckoutFetchFailureWarnsAndStillCompares(t *testing.T) {
	f := newFixture(t)
	env := f.env()
	env.Fetch = func(string, string, string) error { return errors.New("timed out after 30s") }
	var log phaseLog
	if err := GuardCheckout(env, log.report, false, false); err != nil {
		t.Fatalf("refused on a fetch failure alone: %v", err)
	}
	if !log.has("warn", "could not refresh origin/main (timed out after 30s)") {
		t.Fatalf("no fetch warning: %v", log)
	}
}

func TestCheckoutFallsBackWithoutOriginHead(t *testing.T) {
	f := newFixture(t)
	mustGit(t, f.clone, "remote", "set-head", "origin", "-d")
	f.pushRemoteCommit(t)
	s := InspectCheckout(f.env())
	if s.RemoteRef != "origin/main" || s.Behind != 1 {
		t.Fatalf("want origin/main fallback and behind=1, got %+v", s)
	}
}

func TestCheckoutDetachedWorktreeAtOriginMain(t *testing.T) {
	f := newFixture(t)
	f.pushRemoteCommit(t)
	mustGit(t, f.clone, "fetch", "-q", "origin")
	wt := filepath.Join(t.TempDir(), "wt")
	mustGit(t, f.clone, "worktree", "add", "-q", "--detach", wt, "origin/main")
	env := CheckoutEnv{RepoRoot: filepath.Join(wt, "project"), Runner: gitRunner{}, Fetch: realFetch}
	if err := GuardCheckout(env, nil, false, false); err != nil {
		t.Fatalf("a fresh detached worktree at origin/main was refused: %v", err)
	}
}

func TestCheckoutNotAGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	var log phaseLog
	env := CheckoutEnv{RepoRoot: t.TempDir(), Runner: gitRunner{}}
	if err := GuardCheckout(env, log.report, false, false); err != nil {
		t.Fatalf("non-git project refused: %v", err)
	}
	if !log.has("warn", "not a git checkout") {
		t.Fatalf("no warning: %v", log)
	}
}
