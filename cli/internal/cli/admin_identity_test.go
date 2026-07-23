package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/epistola-app/portablevps/internal/config"
)

func touchTestFile(t *testing.T, root, rel string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolveAdminIdentityPrefersPerServerFiles(t *testing.T) {
	t.Setenv("CLOUD_ADMIN_KEY", "")
	t.Setenv("CLOUD_ADMIN_PUBKEY", "")
	root := t.TempDir()
	touchTestFile(t, root, "keys/servers/web-admin.pub")
	touchTestFile(t, root, ".local/ssh/servers/web_ed25519")
	touchTestFile(t, root, legacyAdminPublicRel)
	touchTestFile(t, root, legacyAdminPrivateRel)

	got := resolveAdminIdentity(&config.Context{RepoRoot: root}, "web", "", "")
	if got.publicRel != "keys/servers/web-admin.pub" {
		t.Fatalf("publicRel = %q", got.publicRel)
	}
	if got.privateRel != ".local/ssh/servers/web_ed25519" {
		t.Fatalf("privateRel = %q", got.privateRel)
	}
}

func TestResolveAdminIdentityFallsBackToLegacyCloudAdmin(t *testing.T) {
	t.Setenv("CLOUD_ADMIN_KEY", "")
	t.Setenv("CLOUD_ADMIN_PUBKEY", "")
	root := t.TempDir()
	touchTestFile(t, root, legacyAdminPublicRel)
	touchTestFile(t, root, legacyAdminPrivateRel)

	got := resolveAdminIdentity(&config.Context{RepoRoot: root}, "web", "", "")
	if got.publicRel != legacyAdminPublicRel {
		t.Fatalf("publicRel = %q", got.publicRel)
	}
	if got.privateRel != legacyAdminPrivateRel {
		t.Fatalf("privateRel = %q", got.privateRel)
	}
}

func TestResolveAdminIdentityExplicitPrivateBypassesAuto(t *testing.T) {
	t.Setenv("CLOUD_ADMIN_KEY", "")
	t.Setenv("CLOUD_ADMIN_PUBKEY", "")
	root := t.TempDir()
	touchTestFile(t, root, "keys/servers/web-admin.pub")
	touchTestFile(t, root, ".local/ssh/servers/web_ed25519")

	got := resolveAdminIdentity(&config.Context{RepoRoot: root}, "web", "/tmp/operator-key", "")
	if got.privateRel != "/tmp/operator-key" {
		t.Fatalf("privateRel = %q", got.privateRel)
	}
	if got.publicRel != "" {
		t.Fatalf("publicRel = %q", got.publicRel)
	}
}

func TestResolveAdminIdentityPublicOverridePairsPerServerPrivate(t *testing.T) {
	t.Setenv("CLOUD_ADMIN_KEY", "")
	t.Setenv("CLOUD_ADMIN_PUBKEY", "")
	root := t.TempDir()
	touchTestFile(t, root, ".local/ssh/servers/web_ed25519")

	got := resolveAdminIdentity(&config.Context{RepoRoot: root}, "web", "", "keys/servers/web-admin.pub")
	if got.publicRel != "keys/servers/web-admin.pub" {
		t.Fatalf("publicRel = %q", got.publicRel)
	}
	if got.privateRel != ".local/ssh/servers/web_ed25519" {
		t.Fatalf("privateRel = %q", got.privateRel)
	}
}

func TestNixosAnywhereIdentityUsesPerServerAgentPublicKey(t *testing.T) {
	t.Setenv("CLOUD_ADMIN_KEY", "")
	t.Setenv("CLOUD_ADMIN_PUBKEY", "")
	t.Setenv("PORTABLEVPS_1P_AGENT_SOCK", "/tmp/onepassword.sock")
	root := t.TempDir()
	pubPath := filepath.Join(root, "keys/servers/web-admin.pub")
	touchTestFile(t, root, "keys/servers/web-admin.pub")
	if err := os.Chmod(pubPath, 0o644); err != nil {
		t.Fatal(err)
	}
	touchTestFile(t, root, ".local/ssh/servers/web_ed25519")
	ctx := &config.Context{RepoRoot: root, Vault: "Epistola"}

	opts, cleanup, err := nixosAnywhereIdentity(&globalOptions{interactiveFlag: true}, ctx, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	joined := strings.Join(opts, " ")
	if !strings.Contains(joined, "--ssh-option IdentityAgent=/tmp/onepassword.sock") {
		t.Fatalf("missing 1Password agent option: %v", opts)
	}
	var selector string
	for i, o := range opts {
		if o == "-i" {
			selector = opts[i+1]
		}
	}
	if selector == "" || selector == pubPath {
		t.Fatalf("expected temp per-server public key selector: %v", opts)
	}
	if !strings.HasSuffix(selector, ".pub") {
		t.Fatalf("temp per-server public key selector must keep .pub suffix for OpenSSH, got %q", selector)
	}
	data, err := os.ReadFile(selector)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "test\n" {
		t.Fatalf("selector content = %q", data)
	}
	info, err := os.Stat(selector)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("selector mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestNixosAnywhereIdentityUsesPerServerFileFallback(t *testing.T) {
	t.Setenv("CLOUD_ADMIN_KEY", "")
	t.Setenv("CLOUD_ADMIN_PUBKEY", "")
	root := t.TempDir()
	touchTestFile(t, root, "keys/servers/web-admin.pub")
	touchTestFile(t, root, ".local/ssh/servers/web_ed25519")
	ctx := &config.Context{RepoRoot: root}

	opts, cleanup, err := nixosAnywhereIdentity(&globalOptions{nonInteractive: true}, ctx, "web", "", "")
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	want := "-i " + filepath.Join(root, ".local/ssh/servers/web_ed25519")
	if !strings.Contains(strings.Join(opts, " "), want) {
		t.Fatalf("missing per-server private key fallback %q: %v", want, opts)
	}
}
