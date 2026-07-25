package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type streamRecorder struct{ args []string }

func (s *streamRecorder) Stream(_ string, _ map[string]string, name string, args ...string) error {
	s.args = append([]string{name}, args...)
	return nil
}

func verifiedHostKeyFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte("1.2.3.4 ssh-ed25519 AAAATEST\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNixosAnywhereArgs(t *testing.T) {
	args := nixosAnywhereArgs(
		"/dir", "web", "22", "/tmp/ef", "/tmp/known_hosts",
		[]string{"-i", "/key"}, true, false, "root@1.2.3.4",
	)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"run " + NixosAnywhereFlake,
		"--flake /dir#web",
		"--extra-files /tmp/ef",
		"StrictHostKeyChecking=yes",
		"UserKnownHostsFile=/tmp/known_hosts",
		"-i /key",
		"--build-on-remote",
		"root@1.2.3.4",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %v", want, args)
		}
	}
}

func TestInstallShipsAgeKeyAndRunsNixosAnywhere(t *testing.T) {
	rec := &streamRecorder{}
	err := Install(InstallEnv{FlakeDir: "/dir", Stream: rec}, InstallOpts{
		Server:         "web",
		Target:         "root@1.2.3.4",
		AgeKeyMaterial: "AGE-SECRET-KEY-1TEST",
		IdentityArgs:   []string{"-i", "/key"},
		BuildOnRemote:  true,
		HostKeyFile:    verifiedHostKeyFile(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(rec.args, " ")
	if !strings.Contains(joined, "--flake /dir#web") || !strings.Contains(joined, "--build-on-remote") {
		t.Errorf("nixos-anywhere not invoked correctly: %v", rec.args)
	}
	// the extra-files temp dir is cleaned up after the run
	if strings.Contains(joined, "/nonexistent") {
		t.Errorf("unexpected path")
	}
}

func TestInstallRestoreModeProfile(t *testing.T) {
	rec := &streamRecorder{}
	_ = Install(InstallEnv{FlakeDir: "/dir", Stream: rec}, InstallOpts{
		Server: "web", Target: "root@h", AgeKeyMaterial: "k", RestoreMode: true,
		HostKeyFile: verifiedHostKeyFile(t),
	})
	if !strings.Contains(strings.Join(rec.args, " "), "#web-restore") {
		t.Errorf("restore mode should target web-restore: %v", rec.args)
	}
}

func TestInstallRequiresAgeKey(t *testing.T) {
	err := Install(InstallEnv{FlakeDir: "/dir", Stream: &streamRecorder{}}, InstallOpts{Server: "web", Target: "root@h"})
	if e, ok := err.(*ProvisionError); !ok || e.ExitCode() != 66 {
		t.Fatalf("expected exit 66 for missing age key, got %v", err)
	}
}

func TestInstallRequiresVerifiedHostKey(t *testing.T) {
	err := Install(InstallEnv{FlakeDir: "/dir", Stream: &streamRecorder{}}, InstallOpts{
		Server: "web", Target: "root@h", AgeKeyMaterial: "k",
	})
	if e, ok := err.(*ProvisionError); !ok || e.ExitCode() != 64 {
		t.Fatalf("expected exit 64 for missing host key, got %v", err)
	}
}

func TestInstallAllowsExplicitInsecureSSH(t *testing.T) {
	rec := &streamRecorder{}
	err := Install(InstallEnv{FlakeDir: "/dir", Stream: rec}, InstallOpts{
		Server: "web", Target: "root@h", AgeKeyMaterial: "k", InsecureSSH: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(rec.args, " ")
	for _, want := range []string{"StrictHostKeyChecking=no", "UserKnownHostsFile=/dev/null"} {
		if !strings.Contains(joined, want) {
			t.Errorf("insecure args missing %q: %v", want, rec.args)
		}
	}
}

func TestStageFlakePreservesLockAndOnlyRefreshesPathInput(t *testing.T) {
	workspace := t.TempDir()
	repo := filepath.Join(workspace, "consumer")
	sibling := filepath.Join(workspace, "portablevps")
	for _, path := range []string{repo, sibling} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	flake := `{ inputs.portablevps.url = "path:../portablevps"; }`
	lock := `{"nodes":{"nixpkgs":{"locked":{"rev":"keep-this-pin"}}}}`
	if err := os.WriteFile(filepath.Join(repo, "flake.nix"), []byte(flake), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "flake.lock"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "flake.nix"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := &fakeEnvRunner{}
	staged, cleanup, err := stageFlake(repo, runner)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	gotLock, err := os.ReadFile(filepath.Join(staged, "flake.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotLock) != lock {
		t.Fatalf("committed lock changed before Nix refreshed the path input: %s", gotLock)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected one lock command, got %v", runner.calls)
	}
	joined := strings.Join(runner.calls[0], " ")
	for _, want := range []string{"nix", "flake lock", "--update-input portablevps", staged} {
		if !strings.Contains(joined, want) {
			t.Errorf("lock command missing %q: %v", want, runner.calls[0])
		}
	}
}
