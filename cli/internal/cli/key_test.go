package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/epistola-app/portablevps/internal/config"
)

// TestAgeKeyFieldMatchesKeystoreContract locks the escrowed field's label to
// "age-key" — the keystore reads it as op://<vault>/<server>/age-key, so a
// rename here would silently break key resolution after escrow.
func TestAgeKeyFieldMatchesKeystoreContract(t *testing.T) {
	f := ageKeyField("AGE-SECRET-KEY-EXAMPLE")
	if f["label"] != "age-key" {
		t.Errorf("field label = %v, want age-key (keystore reads /age-key)", f["label"])
	}
	if f["type"] != "CONCEALED" {
		t.Errorf("field type = %v, want CONCEALED", f["type"])
	}
	if f["value"] != "AGE-SECRET-KEY-EXAMPLE" {
		t.Errorf("field value not carried through: %v", f["value"])
	}
}

func fakeSSHKeygen(t *testing.T) *int {
	t.Helper()
	calls := 0
	old := runSSHKeygen
	runSSHKeygen = func(privatePath, comment string) error {
		calls++
		if comment != "portablevps-web-admin" {
			t.Fatalf("comment = %q", comment)
		}
		if err := os.WriteFile(privatePath, []byte("PRIVATE\n"), 0o600); err != nil {
			return err
		}
		return os.WriteFile(privatePath+".pub", []byte("PUBLIC\n"), 0o644)
	}
	t.Cleanup(func() { runSSHKeygen = old })
	return &calls
}

func TestGenerateServerAdminKeypairCreatesPerServerFiles(t *testing.T) {
	calls := fakeSSHKeygen(t)
	root := t.TempDir()
	ctx := &config.Context{RepoRoot: root}

	pair, err := generateServerAdminKeypair(ctx, "web", false)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatalf("ssh-keygen calls = %d, want 1", *calls)
	}
	if pair.privateRel != filepath.Join(".local", "ssh", "servers", "web_ed25519") {
		t.Fatalf("privateRel = %q", pair.privateRel)
	}
	if pair.publicRel != filepath.Join("keys", "servers", "web-admin.pub") {
		t.Fatalf("publicRel = %q", pair.publicRel)
	}

	privatePath := filepath.Join(root, pair.privateRel)
	publicPath := filepath.Join(root, pair.publicRel)
	if got, err := os.ReadFile(privatePath); err != nil || string(got) != "PRIVATE\n" {
		t.Fatalf("private key = %q, %v", got, err)
	}
	if got, err := os.ReadFile(publicPath); err != nil || string(got) != "PUBLIC\n" {
		t.Fatalf("public key = %q, %v", got, err)
	}
	if fileExistsCli(privatePath + ".pub") {
		t.Fatalf("temporary public key was not removed")
	}
	if mode := fileMode(t, privatePath); mode != 0o600 {
		t.Fatalf("private key mode = %#o, want 0600", mode)
	}
	if mode := fileMode(t, publicPath); mode != 0o644 {
		t.Fatalf("public key mode = %#o, want 0644", mode)
	}
}

func TestGenerateServerAdminKeypairRefusesExistingWithoutForce(t *testing.T) {
	calls := fakeSSHKeygen(t)
	root := t.TempDir()
	touchTestFile(t, root, filepath.Join("keys", "servers", "web-admin.pub"))

	_, err := generateServerAdminKeypair(&config.Context{RepoRoot: root}, "web", false)
	if err == nil {
		t.Fatal("expected existing key error")
	}
	if *calls != 0 {
		t.Fatalf("ssh-keygen calls = %d, want 0", *calls)
	}
}

func TestEnsureServerAdminKeypairNoopsWhenComplete(t *testing.T) {
	calls := fakeSSHKeygen(t)
	root := t.TempDir()
	touchTestFile(t, root, filepath.Join(".local", "ssh", "servers", "web_ed25519"))
	touchTestFile(t, root, filepath.Join("keys", "servers", "web-admin.pub"))

	created, _, err := ensureServerAdminKeypair(&config.Context{RepoRoot: root}, "web")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("created = true, want false")
	}
	if *calls != 0 {
		t.Fatalf("ssh-keygen calls = %d, want 0", *calls)
	}
}

func TestEnsureServerAdminKeypairRejectsPartialState(t *testing.T) {
	calls := fakeSSHKeygen(t)
	root := t.TempDir()
	touchTestFile(t, root, filepath.Join("keys", "servers", "web-admin.pub"))

	_, _, err := ensureServerAdminKeypair(&config.Context{RepoRoot: root}, "web")
	if err == nil {
		t.Fatal("expected partial keypair error")
	}
	if *calls != 0 {
		t.Fatalf("ssh-keygen calls = %d, want 0", *calls)
	}
}

func TestShouldAutoGenerateAdminKeyHonorsOverrides(t *testing.T) {
	t.Setenv("CLOUD_ADMIN_KEY", "")
	t.Setenv("CLOUD_ADMIN_PUBKEY", "")
	if !shouldAutoGenerateAdminKey("") {
		t.Fatal("default path should auto-generate")
	}
	if shouldAutoGenerateAdminKey("keys/custom.pub") {
		t.Fatal("admin public override should disable auto-generation")
	}
	t.Setenv("CLOUD_ADMIN_KEY", "/tmp/key")
	if shouldAutoGenerateAdminKey("") {
		t.Fatal("CLOUD_ADMIN_KEY should disable auto-generation")
	}
	t.Setenv("CLOUD_ADMIN_KEY", "")
	t.Setenv("CLOUD_ADMIN_PUBKEY", "keys/custom.pub")
	if shouldAutoGenerateAdminKey("") {
		t.Fatal("CLOUD_ADMIN_PUBKEY should disable auto-generation")
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
