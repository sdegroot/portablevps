package core

import (
	"errors"
	"testing"
)

func pathEnv(vars map[string]string) (func(string) string, func(string, string) error) {
	return func(k string) string { return vars[k] },
		func(k, v string) error { vars[k] = v; return nil }
}

func TestEnsureNixOnPathLeavesAWorkingPathAlone(t *testing.T) {
	vars := map[string]string{"PATH": "/usr/bin"}
	getenv, setenv := pathEnv(vars)
	found := func(string) (string, error) { return "/usr/bin/nix", nil }
	if dir := EnsureNixOnPath(found, getenv, setenv, func(string) bool { return true }); dir != "" {
		t.Fatalf("nix is on PATH; expected no change, got %q", dir)
	}
	if vars["PATH"] != "/usr/bin" {
		t.Errorf("PATH changed: %q", vars["PATH"])
	}
}

func TestEnsureNixOnPathPrependsTheDaemonProfile(t *testing.T) {
	vars := map[string]string{"PATH": "/usr/bin", "HOME": "/Users/op"}
	getenv, setenv := pathEnv(vars)
	missing := func(string) (string, error) { return "", errors.New("not found") }
	exists := func(p string) bool { return p == "/nix/var/nix/profiles/default/bin/nix" }
	dir := EnsureNixOnPath(missing, getenv, setenv, exists)
	if dir != "/nix/var/nix/profiles/default/bin" {
		t.Fatalf("expected the daemon profile, got %q", dir)
	}
	if vars["PATH"] != "/nix/var/nix/profiles/default/bin:/usr/bin" {
		t.Errorf("expected the profile prepended so children find nix too, got %q", vars["PATH"])
	}
}

func TestEnsureNixOnPathFallsBackToTheUserProfile(t *testing.T) {
	vars := map[string]string{"PATH": "/usr/bin", "HOME": "/home/op"}
	getenv, setenv := pathEnv(vars)
	missing := func(string) (string, error) { return "", errors.New("not found") }
	exists := func(p string) bool { return p == "/home/op/.nix-profile/bin/nix" }
	if dir := EnsureNixOnPath(missing, getenv, setenv, exists); dir != "/home/op/.nix-profile/bin" {
		t.Fatalf("expected the single-user profile, got %q", dir)
	}
}

func TestEnsureNixOnPathWithNoNixAnywhere(t *testing.T) {
	vars := map[string]string{"PATH": "/usr/bin"}
	getenv, setenv := pathEnv(vars)
	missing := func(string) (string, error) { return "", errors.New("not found") }
	if dir := EnsureNixOnPath(missing, getenv, setenv, func(string) bool { return false }); dir != "" {
		t.Fatalf("expected nothing added, got %q", dir)
	}
	if vars["PATH"] != "/usr/bin" {
		t.Errorf("PATH changed: %q", vars["PATH"])
	}
}
