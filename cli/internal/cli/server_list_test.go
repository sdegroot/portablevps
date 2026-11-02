package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// listFixture registers two servers: one with both public addresses recorded,
// one with neither (and no purpose), so each test sees both rendering paths.
func listFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	registry := filepath.Join(dir, "registry.json")
	data := `{
		"runner": {"name":"runner","placement":{"provider":"leaseweb","publicIPv4":"95.211.45.24","publicIPv6":"2001:db8::791"},"purpose":"CI runner"},
		"spare":  {"name":"spare","placement":{"provider":"hetzner","publicIPv4":null,"publicIPv6":null}}
	}`
	if err := os.WriteFile(registry, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SERVER_REGISTRY", registry)
	return dir
}

func runServerList(t *testing.T, g *globalOptions) string {
	t.Helper()
	cmd := newServerListCmd(g)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestServerListTableShowsPublicAddresses(t *testing.T) {
	out := runServerList(t, &globalOptions{project: listFixture(t)})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want header + 2 rows, got %d lines:\n%s", len(lines), out)
	}
	if got := strings.Fields(lines[0]); strings.Join(got, " ") != "NAME PROVIDER PUBLIC IPV4 PUBLIC IPV6 PURPOSE" {
		t.Errorf("header = %q", lines[0])
	}
	if got := strings.Fields(lines[1]); strings.Join(got, " ") != "runner leaseweb 95.211.45.24 2001:db8::791 CI runner" {
		t.Errorf("runner row = %q", lines[1])
	}
	// unrecorded addresses render as "-" so the columns stay aligned.
	if got := strings.Fields(lines[2]); strings.Join(got, " ") != "spare hetzner - - (no purpose set)" {
		t.Errorf("spare row = %q", lines[2])
	}
}

func TestServerListJSONIncludesPublicAddresses(t *testing.T) {
	out := runServerList(t, &globalOptions{project: listFixture(t), json: true})
	var rows []map[string]string
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if rows[0]["publicIPv4"] != "95.211.45.24" || rows[0]["publicIPv6"] != "2001:db8::791" {
		t.Errorf("runner = %v", rows[0])
	}
	// unset stays an empty string in JSON (the "-" is table-only).
	if v, ok := rows[1]["publicIPv4"]; !ok || v != "" {
		t.Errorf("spare publicIPv4 = %q (present %v), want empty", v, ok)
	}
}
