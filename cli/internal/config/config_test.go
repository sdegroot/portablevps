package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeToml(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func noEnv(string) string { return "" }

func TestResolveDefaultsFromRepoToml(t *testing.T) {
	root := t.TempDir()
	writeToml(t, filepath.Join(root, "portablevps.toml"), `
default_server = "web-1"
dns_zone = "int.example.net"
[secrets]
op_account = "acme.1password.com"
[network]
api_token = "op://Vault/NetBird/token"
[provider.hetzner]
token = "op://Vault/Hetzner/token"
`)
	ctx, err := Resolve(Flags{Project: root}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Server != "web-1" {
		t.Errorf("server = %q", ctx.Server)
	}
	if ctx.DNSZone != "int.example.net" {
		t.Errorf("dns zone = %q", ctx.DNSZone)
	}
	if ctx.OpAccount != "acme.1password.com" {
		t.Errorf("op account = %q", ctx.OpAccount)
	}
	if ctx.Network.APIToken != "op://Vault/NetBird/token" {
		t.Errorf("netbird token = %q", ctx.Network.APIToken)
	}
	if ctx.Providers["hetzner"].Token != "op://Vault/Hetzner/token" {
		t.Errorf("hetzner token = %q", ctx.Providers["hetzner"].Token)
	}
}

func TestPrecedenceFlagOverEnvOverLocalOverRepo(t *testing.T) {
	root := t.TempDir()
	writeToml(t, filepath.Join(root, "portablevps.toml"), `default_server = "repo"`)
	writeToml(t, filepath.Join(root, ".local", "portablevps.local.toml"), `default_server = "local"`)

	// repo only
	ctx, _ := Resolve(Flags{Project: root}, noEnv)
	if ctx.Server != "local" { // local overrides repo
		t.Errorf("local should override repo, got %q", ctx.Server)
	}

	// env overrides local
	env := func(k string) string {
		if k == "SERVER" {
			return "env"
		}
		return ""
	}
	ctx, _ = Resolve(Flags{Project: root}, env)
	if ctx.Server != "env" {
		t.Errorf("env should override local, got %q", ctx.Server)
	}

	// flag overrides env
	ctx, _ = Resolve(Flags{Project: root, Server: "flag"}, env)
	if ctx.Server != "flag" {
		t.Errorf("flag should override env, got %q", ctx.Server)
	}
}

func TestMissingTomlIsNotAnError(t *testing.T) {
	root := t.TempDir()
	ctx, err := Resolve(Flags{Project: root}, noEnv)
	if err != nil {
		t.Fatalf("missing toml should be fine: %v", err)
	}
	if ctx.Server != "" {
		t.Errorf("no default server expected, got %q", ctx.Server)
	}
}
