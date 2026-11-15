package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestAdminKeyFieldMatchesKeystoreContract locks the label to "private key":
// the keystore reads op://<vault>/<server>/private key, so a rename here would
// silently break headless SSH after escrow.
func TestAdminKeyFieldMatchesKeystoreContract(t *testing.T) {
	f := adminKeyField("-----BEGIN OPENSSH PRIVATE KEY-----\nX\n-----END OPENSSH PRIVATE KEY-----")
	if f["label"] != "private key" {
		t.Errorf("label = %v, want \"private key\" (keystore reads /private key)", f["label"])
	}
	if f["type"] != "CONCEALED" {
		t.Errorf("type = %v, want CONCEALED", f["type"])
	}
	if f["id"] == "age-key" {
		t.Error("id must differ from the age-key field, or one would overwrite the other")
	}
}

func TestSamePublicKeyIgnoresComment(t *testing.T) {
	a := "ssh-ed25519 AAAAkey portablevps-web-admin"
	if !samePublicKey(a, "ssh-ed25519 AAAAkey other-comment\n") {
		t.Error("same type and key with a different comment should match")
	}
	if samePublicKey(a, "ssh-ed25519 AAAAother portablevps-web-admin") {
		t.Error("different key material must not match")
	}
	if samePublicKey(a, "ssh-rsa AAAAkey") {
		t.Error("different key type must not match")
	}
	if samePublicKey("", a) || samePublicKey(a, "garbage") {
		t.Error("malformed lines must not match")
	}
}

// fakeDerive makes deriveSSHPublicKey return pub (or fail), and records the
// content of the file it was given.
func fakeDerive(t *testing.T, pub string, err error) *string {
	t.Helper()
	var seen string
	old := deriveSSHPublicKey
	deriveSSHPublicKey = func(path string) (string, error) {
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatal(rerr)
		}
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0o600 {
			t.Errorf("verify temp file mode = %v, want 0600", info.Mode().Perm())
		}
		seen = string(data)
		return pub, err
	}
	t.Cleanup(func() { deriveSSHPublicKey = old })
	return &seen
}

func TestVerifyAdminKeyReadBack(t *testing.T) {
	committed := "ssh-ed25519 AAAAkey portablevps-web-admin\n"

	seen := fakeDerive(t, "ssh-ed25519 AAAAkey\n", nil)
	// 1Password may drop the trailing newline; the check normalises it the way
	// the keystore's temp file does.
	if err := verifyAdminKeyReadBack("PRIVATE", committed); err != nil {
		t.Fatalf("matching key: %v", err)
	}
	if *seen != "PRIVATE\n" {
		t.Errorf("temp file content = %q, want exactly one trailing newline", *seen)
	}

	fakeDerive(t, "ssh-ed25519 AAAAother\n", nil)
	if err := verifyAdminKeyReadBack("PRIVATE", committed); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("mismatched key: err = %v, want a mismatch", err)
	}

	fakeDerive(t, "", errors.New("invalid format"))
	if err := verifyAdminKeyReadBack("truncated", committed); err == nil || !strings.Contains(err.Error(), "not a usable") {
		t.Errorf("unusable key: err = %v, want 'not a usable'", err)
	}
}

// TestVerifyAdminKeyReadBackRealKey runs the check against a real ed25519 pair,
// including the read-back shape 1Password returns (no trailing newline).
func TestVerifyAdminKeyReadBackRealKey(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not on PATH")
	}
	dir := t.TempDir()
	priv := filepath.Join(dir, "k")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "test", "-f", priv).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	key, _ := os.ReadFile(priv)
	pub, _ := os.ReadFile(priv + ".pub")
	if err := verifyAdminKeyReadBack(strings.TrimRight(string(key), "\n"), string(pub)); err != nil {
		t.Fatalf("real pair: %v", err)
	}
	other := filepath.Join(dir, "o")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", other).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	otherPub, _ := os.ReadFile(other + ".pub")
	if err := verifyAdminKeyReadBack(string(key), string(otherPub)); err == nil {
		t.Fatal("a key must not verify against another key's public half")
	}
}
