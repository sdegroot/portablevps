package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/epistola-app/portablevps/internal/sopsconfig"
)

// fakeEnvRunner simulates age-keygen (writing a key file, deriving a recipient)
// and records sops calls, so SecretInit's orchestration is tested without
// touching real tools.
type fakeEnvRunner struct{ calls [][]string }

func (f *fakeEnvRunner) Run(_ , name string, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if name == "age-keygen" && len(args) >= 2 && args[0] == "-o" {
		_ = os.WriteFile(args[1], []byte("AGE-SECRET-KEY-1TEST\n"), 0o600)
		return "", nil
	}
	if name == "age-keygen" && len(args) >= 2 && args[0] == "-y" {
		return "age1recipientfor-" + filepath.Base(filepath.Dir(args[1])), nil
	}
	return "", nil
}

func (f *fakeEnvRunner) RunEnv(_ string, _ map[string]string, name string, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return "", nil
}

func (f *fakeEnvRunner) called(name string) bool {
	for _, c := range f.calls {
		if c[0] == name {
			return true
		}
	}
	return false
}

func newSecretEnv(t *testing.T) (SecretEnv, *fakeEnvRunner, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "keys"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "keys", "operator-age.pub"), []byte("age1operator\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fr := &fakeEnvRunner{}
	return SecretEnv{RepoRoot: root, Runner: fr}, fr, root
}

func TestSecretInitOwnsTheCeremony(t *testing.T) {
	env, fr, root := newSecretEnv(t)

	res, err := SecretInit(env, "web", false)
	if err != nil {
		t.Fatal(err)
	}

	// key generated
	if !fileExists(filepath.Join(root, res.KeyPath)) {
		t.Errorf("key not generated at %s", res.KeyPath)
	}
	// .sops.yaml rule written with operator + per-server recipient
	rules, err := sopsconfig.Rules(filepath.Join(root, ".sops.yaml"))
	if err != nil || len(rules) != 1 {
		t.Fatalf("rules = %+v err=%v", rules, err)
	}
	if rules[0].PathRegex != `secrets/web\.yaml$` {
		t.Errorf("path_regex = %q", rules[0].PathRegex)
	}
	if rules[0].Age != "age1operator,age1recipientfor-web" {
		t.Errorf("age = %q", rules[0].Age)
	}
	// no secrets file yet -> no rekey
	if res.Rekeyed || fr.called("sops") {
		t.Errorf("should not have rekeyed a nonexistent secrets file")
	}
}

func TestSecretInitRefusesExistingKeyWithoutForce(t *testing.T) {
	env, _, root := newSecretEnv(t)
	keyPath := filepath.Join(root, PerServerKeyRel("web"))
	_ = os.MkdirAll(filepath.Dir(keyPath), 0o700)
	_ = os.WriteFile(keyPath, []byte("existing"), 0o600)

	_, err := SecretInit(env, "web", false)
	var se *SecretError
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected refusal, got %v", err)
	}
	if e, ok := err.(*SecretError); ok {
		se = e
	}
	if se == nil || se.ExitCode() != 73 {
		t.Fatalf("expected exit 73, got %v", err)
	}
}

func TestSecretInitRekeysExistingSecretsFile(t *testing.T) {
	env, fr, root := newSecretEnv(t)
	_ = os.MkdirAll(filepath.Join(root, "secrets"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "secrets", "web.yaml"), []byte("enc"), 0o644)

	res, err := SecretInit(env, "web", false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Rekeyed {
		t.Errorf("expected rekey of existing secrets file")
	}
	if !fr.called("sops") {
		t.Errorf("expected sops updatekeys to run")
	}
}
