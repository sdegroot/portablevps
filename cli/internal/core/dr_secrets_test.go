package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sdegroot/portablevps/internal/adapters"
	"github.com/sdegroot/portablevps/internal/sopsconfig"
)

// requireTool skips the test if name isn't on PATH (sops/age/git — all real
// dev-environment tools per mise.toml, but keep CI from hard-failing if one
// is ever missing).
func requireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not on PATH: %v", name, err)
	}
}

// repoSecretsFixture builds a real git repo with two age identities
// registered in .sops.yaml (source and target) and identity's secrets file
// encrypted to source's recipient, committed clean. Returns the repo root
// and each identity's private SOPS_AGE_KEY material.
func repoSecretsFixture(t *testing.T) (repoRoot, sourceKey, targetKey string) {
	t.Helper()
	requireTool(t, "git")
	requireTool(t, "age-keygen")
	requireTool(t, "sops")

	repoRoot = t.TempDir()
	runner := adapters.ExecRunner{}
	run := func(name string, args ...string) string {
		t.Helper()
		out, err := runner.Run(repoRoot, name, args...)
		if err != nil {
			t.Fatalf("%s %v: %v (%s)", name, args, err, out)
		}
		return out
	}
	run("git", "init", "-q")
	run("git", "config", "user.email", "test@example.com")
	run("git", "config", "user.name", "test")

	genKey := func(rel string) (privateMaterial, recipient string) {
		path := filepath.Join(repoRoot, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		run("age-keygen", "-o", path)
		material, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		recipient = strings.TrimSpace(run("age-keygen", "-y", path))
		return string(material), recipient
	}
	sourceKey, sourceRecipient := genKey(".local/sops/servers/source/age-key.txt")
	targetKey, targetRecipient := genKey(".local/sops/servers/target/age-key.txt")

	sopsPath := filepath.Join(repoRoot, ".sops.yaml")
	if err := sopsconfig.UpsertRule(sopsPath, sopsconfig.Rule{PathRegex: `secrets/source\.yaml$`, Age: sourceRecipient}); err != nil {
		t.Fatal(err)
	}
	if err := sopsconfig.UpsertRule(sopsPath, sopsconfig.Rule{PathRegex: `secrets/target\.yaml$`, Age: targetRecipient}); err != nil {
		t.Fatal(err)
	}

	secretsRel := filepath.Join("secrets", "source.yaml")
	secretsPath := filepath.Join(repoRoot, secretsRel)
	if err := os.MkdirAll(filepath.Dir(secretsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretsPath, []byte("postgres:\n  password: hunter2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("sops", "--encrypt", "--in-place", "--age", sourceRecipient, secretsRel)

	run("git", "add", "-A")
	run("git", "commit", "-q", "-m", "fixture")
	return repoRoot, sourceKey, targetKey
}

func decryptWith(t *testing.T, repoRoot, key, rel string) (string, error) {
	t.Helper()
	runner := adapters.ExecRunner{}
	return runner.RunEnv(repoRoot, map[string]string{"SOPS_AGE_KEY": key}, "sops", "-d", rel)
}

func TestRepoSecretsSwapReencryptsForTargetAndRevertRestoresOriginal(t *testing.T) {
	repoRoot, sourceKey, targetKey := repoSecretsFixture(t)
	s := RepoSecrets{RepoRoot: repoRoot, Runner: adapters.ExecRunner{}}

	original, err := os.ReadFile(filepath.Join(repoRoot, "secrets", "source.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	revert, err := s.Swap("source", "target", map[string]string{"SOPS_AGE_KEY": sourceKey})
	if err != nil {
		t.Fatalf("Swap: %v", err)
	}
	if revert == nil {
		t.Fatal("expected a non-nil revert func (source has a secrets file)")
	}

	// The target's own key can now decrypt it...
	out, err := decryptWith(t, repoRoot, targetKey, "secrets/source.yaml")
	if err != nil {
		t.Fatalf("target decrypt after swap: %v", err)
	}
	if !strings.Contains(out, "hunter2") {
		t.Fatalf("decrypted content missing expected secret: %q", out)
	}

	// ...and the source's own key can no longer decrypt it (only the
	// target's recipient was used to re-encrypt — the whole point).
	if _, err := decryptWith(t, repoRoot, sourceKey, "secrets/source.yaml"); err == nil {
		t.Fatal("expected source's own key to fail decrypting the target-encrypted swap")
	}

	if err := revert(); err != nil {
		t.Fatalf("revert: %v", err)
	}

	restored, err := os.ReadFile(filepath.Join(repoRoot, "secrets", "source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(original) {
		t.Fatalf("revert did not restore the exact original bytes:\noriginal: %s\nrestored: %s", original, restored)
	}
	// Source's own key decrypts again post-revert.
	if _, err := decryptWith(t, repoRoot, sourceKey, "secrets/source.yaml"); err != nil {
		t.Fatalf("source decrypt after revert: %v", err)
	}

	// A second revert() call must be a safe no-op (RestoreDrill's defer
	// could in principle be invoked in a retry path).
	if err := revert(); err != nil {
		t.Fatalf("second revert call: %v", err)
	}
}

func TestRepoSecretsSwapNoopWhenIdentityHasNoSecretsFile(t *testing.T) {
	repoRoot, sourceKey, _ := repoSecretsFixture(t)
	s := RepoSecrets{RepoRoot: repoRoot, Runner: adapters.ExecRunner{}}
	revert, err := s.Swap("no-such-identity", "target", map[string]string{"SOPS_AGE_KEY": sourceKey})
	if err != nil {
		t.Fatalf("expected no error for an identity with no secrets file, got %v", err)
	}
	if revert != nil {
		t.Fatal("expected a nil revert func when there is nothing to swap")
	}
}

func TestRepoSecretsSwapFailsWithoutRegisteredTargetRecipient(t *testing.T) {
	repoRoot, sourceKey, _ := repoSecretsFixture(t)
	s := RepoSecrets{RepoRoot: repoRoot, Runner: adapters.ExecRunner{}}
	if _, err := s.Swap("source", "unregistered-host", map[string]string{"SOPS_AGE_KEY": sourceKey}); err == nil {
		t.Fatal("expected an error when the target host has no .sops.yaml recipient")
	}
}

func TestRepoSecretsSwapRefusesADirtySecretsFile(t *testing.T) {
	repoRoot, sourceKey, _ := repoSecretsFixture(t)
	path := filepath.Join(repoRoot, "secrets", "source.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	s := RepoSecrets{RepoRoot: repoRoot, Runner: adapters.ExecRunner{}}
	if _, err := s.Swap("source", "target", map[string]string{"SOPS_AGE_KEY": sourceKey}); err == nil {
		t.Fatal("expected an error swapping a secrets file with uncommitted changes")
	}
}
