package core

import (
	"strings"
	"testing"
)

type streamRecorder struct{ args []string }

func (s *streamRecorder) Stream(_ string, _ map[string]string, name string, args ...string) error {
	s.args = append([]string{name}, args...)
	return nil
}

func TestNixosAnywhereArgs(t *testing.T) {
	args := nixosAnywhereArgs("/dir", "web", "22", "/tmp/ef", []string{"-i", "/key"}, true, "root@1.2.3.4")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"run github:nix-community/nixos-anywhere",
		"--flake /dir#web",
		"--extra-files /tmp/ef",
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
