package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// fakeRunner records calls and returns canned output; core never shells out in
// tests. Doctor uses SERVER_REGISTRY, so the runner should not be called at all.
type fakeRunner struct{ called bool }

func (f *fakeRunner) Run(_, _ string, _ ...string) (string, error) {
	f.called = true
	return "", nil
}

// testEnv builds a doctor Env over a temp repo with a SERVER_REGISTRY fixture.
func testEnv(t *testing.T, servers map[string]map[string]any, present map[string]bool, extraEnv map[string]string) (Env, string) {
	t.Helper()
	root := t.TempDir()

	registry := filepath.Join(root, "servers.json")
	data, err := json.Marshal(servers)
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	if err := os.WriteFile(registry, data, 0o644); err != nil {
		t.Fatalf("write registry: %v", err)
	}

	// flake.nix present by default; git root present by default.
	writeIf(t, present["flake"], filepath.Join(root, "flake.nix"), "{}")
	if present["git"] {
		if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	envVars := map[string]string{"SERVER_REGISTRY": registry}
	for k, v := range extraEnv {
		envVars[k] = v
	}

	env := Env{
		RepoRoot:   root,
		Runner:     &fakeRunner{},
		HasCommand: func(string) bool { return true },
		Getenv:     func(k string) string { return envVars[k] },
	}
	return env, root
}

func writeIf(t *testing.T, cond bool, path, content string) {
	t.Helper()
	if !cond {
		return
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func statuses(checks []Check) map[string]Status {
	m := map[string]Status{}
	for _, c := range checks {
		m[c.Title] = c.Status
	}
	return m
}

func hasFailWith(checks []Check, substr string) bool {
	for _, c := range checks {
		if c.Status == StatusFail && contains(c.Title, substr) {
			return true
		}
	}
	return false
}

func hasWarnWith(checks []Check, substr string) bool {
	for _, c := range checks {
		if c.Status == StatusWarn && contains(c.Title, substr) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestDoctorCleanRepoHasNoFailures(t *testing.T) {
	env, root := testEnv(t,
		map[string]map[string]any{
			"web": {"name": "web", "placement": map[string]any{"provider": "hetzner"}, "backupRepository": "s3://x/web"},
		},
		map[string]bool{"flake": true, "git": true},
		nil,
	)
	// Provide the operator keys + per-server files so nothing warns either.
	mkdirAll(t, filepath.Join(root, ".local/ssh"))
	writeMode(t, filepath.Join(root, ".local/ssh/cloud-admin_ed25519"), "k", 0o600)
	mkdirAll(t, filepath.Join(root, "keys"))
	writeIf(t, true, filepath.Join(root, "keys", "cloud-admin.pub"), "p")
	mkdirAll(t, filepath.Join(root, ".local/sops/servers/web"))
	writeIf(t, true, filepath.Join(root, ".local/sops/servers/web/age-key.txt"), "a")
	mkdirAll(t, filepath.Join(root, "secrets"))
	writeIf(t, true, filepath.Join(root, "secrets/web.yaml"), "e")

	checks := RunDoctor(env, "web")

	if HasFailures(checks) {
		t.Fatalf("expected no failures, got: %+v", checks)
	}
	if _, ok := statuses(checks)["provider \"hetzner\" declared"]; !ok {
		t.Errorf("expected provider check, got %+v", statuses(checks))
	}
	if fr, ok := env.Runner.(*fakeRunner); ok && fr.called {
		t.Errorf("runner should not be called when SERVER_REGISTRY is set")
	}
}

func TestDoctorMissingFlakeIsFailure(t *testing.T) {
	env, _ := testEnv(t,
		map[string]map[string]any{"web": {"placement": map[string]any{"provider": "hetzner"}}},
		map[string]bool{"flake": false, "git": true},
		nil,
	)
	checks := RunDoctor(env, "")
	if !hasFailWith(checks, "flake.nix missing") {
		t.Fatalf("expected flake.nix missing failure, got %+v", checks)
	}
}

func TestDoctorMissingToolIsFailure(t *testing.T) {
	env, _ := testEnv(t,
		map[string]map[string]any{"web": {"placement": map[string]any{"provider": "hetzner"}}},
		map[string]bool{"flake": true, "git": true},
		nil,
	)
	env.HasCommand = func(name string) bool { return name != "nix" }
	checks := RunDoctor(env, "")
	if !hasFailWith(checks, "nix not found") {
		t.Fatalf("expected nix not found failure, got %+v", checks)
	}
}

func TestDoctorUnknownServerIsFailure(t *testing.T) {
	env, _ := testEnv(t,
		map[string]map[string]any{"web": {"placement": map[string]any{"provider": "hetzner"}}},
		map[string]bool{"flake": true, "git": true},
		nil,
	)
	checks := RunDoctor(env, "nope")
	if !hasFailWith(checks, "not found") {
		t.Fatalf("expected unknown-server failure, got %+v", checks)
	}
}

func TestDoctorMissingBackupRepoWarns(t *testing.T) {
	env, _ := testEnv(t,
		map[string]map[string]any{"web": {"placement": map[string]any{"provider": "hetzner"}}},
		map[string]bool{"flake": true, "git": true},
		nil,
	)
	checks := RunDoctor(env, "web")
	if !hasWarnWith(checks, "no backupRepository") {
		t.Fatalf("expected backupRepository warning, got %+v", checks)
	}
}

func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeMode(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorWarnsWhenNixWasFoundOffPath(t *testing.T) {
	env, _ := testEnv(t, map[string]map[string]any{}, map[string]bool{"flake": true, "git": true}, nil)
	env.NixAddedToPath = "/nix/var/nix/profiles/default/bin"
	got := statuses(RunDoctor(env, ""))
	if got["nix not on PATH; pvps uses /nix/var/nix/profiles/default/bin/nix"] != StatusWarn {
		t.Fatalf("expected a nix-off-PATH warning, got %v", got)
	}
	if _, ok := got["nix available"]; ok {
		t.Error("must not also report nix as plainly available")
	}
}

func TestDoctorReportsTheNixosRebuildPin(t *testing.T) {
	saved := NixpkgsFlake
	t.Cleanup(func() { NixpkgsFlake = saved })
	env, _ := testEnv(t, map[string]map[string]any{}, map[string]bool{"flake": true, "git": true}, nil)

	NixpkgsFlake = "nixpkgs"
	if got := statuses(RunDoctor(env, "")); got["nixos-rebuild is not pinned (resolved through your flake registry)"] != StatusWarn {
		t.Fatalf("expected an unpinned warning for a source build, got %v", got)
	}
	NixpkgsFlake = "github:NixOS/nixpkgs/0bb7ec54c8483066ec9d7720e780a5caa71f8612"
	if got := statuses(RunDoctor(env, "")); got["nixos-rebuild pinned to github:NixOS/nixpkgs/0bb7ec54c8483066ec9d7720e780a5caa71f8612"] != StatusOK {
		t.Fatalf("expected the pin reported OK, got %v", got)
	}
}
