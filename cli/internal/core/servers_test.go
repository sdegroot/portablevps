package core

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeGetenv returns a Getenv that maps SERVER_REGISTRY to a fixture file.
func writeRegistry(t *testing.T, json string) func(string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")
	if err := os.WriteFile(path, []byte(json), 0o600); err != nil {
		t.Fatal(err)
	}
	return func(k string) string {
		if k == "SERVER_REGISTRY" {
			return path
		}
		return ""
	}
}

// TestLoadServersParsesPurposeAndProvider verifies the serverInfo fields the CLI
// relies on for `server list`: purpose, provider (from placement fallback), and
// netbird groups.
func TestLoadServersParsesPurposeAndProvider(t *testing.T) {
	getenv := writeRegistry(t, `{
		"web":  {"name":"web","provider":"hetzner","purpose":"Website","netbird":{"groups":["portablevps-servers","web"]}},
		"code": {"name":"code","placement":{"provider":"leaseweb"},"purpose":"Forgejo code hosting"},
		"bare": {"name":"bare","provider":"hetzner"}
	}`)
	servers, err := LoadServers(Env{RepoRoot: "/nonexistent", Getenv: getenv})
	if err != nil {
		t.Fatal(err)
	}
	if got := servers["web"].Purpose; got != "Website" {
		t.Errorf("web purpose = %q, want Website", got)
	}
	if got := servers["web"].NetbirdGroups; len(got) != 2 || got[0] != "portablevps-servers" {
		t.Errorf("web groups = %v", got)
	}
	// provider falls back to placement.provider when the top-level is empty.
	if got := servers["code"].Provider; got != "leaseweb" {
		t.Errorf("code provider = %q, want leaseweb", got)
	}
	// a server with no purpose set parses to an empty string, not an error.
	if got := servers["bare"].Purpose; got != "" {
		t.Errorf("bare purpose = %q, want empty", got)
	}
}
