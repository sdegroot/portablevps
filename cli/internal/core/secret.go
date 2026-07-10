package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/epistola-app/portablevps/internal/sopsconfig"
)

// EnvRunner runs commands, optionally with extra environment (for sops/age which
// read SOPS_AGE_KEY_FILE).
type EnvRunner interface {
	Run(dir, name string, args ...string) (string, error)
	RunEnv(dir string, env map[string]string, name string, args ...string) (string, error)
}

// SecretEnv is the injected environment for the sops/age ceremony.
type SecretEnv struct {
	RepoRoot string
	Runner   EnvRunner
}

// SecretError carries a sysexits-style exit code.
type SecretError struct {
	Code int
	Msg  string
}

func (e *SecretError) Error() string   { return e.Msg }
func (e *SecretError) ExitCode() int   { return e.Code }
func secretErr(code int, format string, a ...any) *SecretError {
	return &SecretError{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// InitResult reports what the ceremony did.
type InitResult struct {
	Recipient   string // the per-server age recipient
	KeyPath     string // repo-relative path of the generated key
	SopsConfig  string // ".sops.yaml"
	SecretsFile string // "secrets/<server>.yaml"
}

// PerServerKeyRel is the repo-relative path of a server's age key.
func PerServerKeyRel(server string) string {
	return filepath.Join(".local", "sops", "servers", server, "age-key.txt")
}

// SecretInit generates the per-server age key and registers it in .sops.yaml as
// the SOLE recipient of that server's secrets (Model A: no operator master key —
// access is controlled by who can reach the key). The operator never hand-edits
// .sops.yaml. The generated key is written to the fallback file; storing it in
// 1Password is a separate step (the CLI layer, once a vault is configured).
func SecretInit(env SecretEnv, server string, force bool) (InitResult, error) {
	var res InitResult
	keyRel := PerServerKeyRel(server)
	keyPath := filepath.Join(env.RepoRoot, keyRel)

	if fileExists(keyPath) && !force {
		return res, secretErr(73, "%s already exists; pass --force to replace it", keyRel)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return res, err
	}
	if fileExists(keyPath) {
		_ = os.Remove(keyPath)
	}
	if _, err := env.Runner.Run("", "age-keygen", "-o", keyPath); err != nil {
		return res, secretErr(70, "age-keygen failed: %v", err)
	}
	_ = os.Chmod(keyPath, 0o600)

	recipient, err := env.Runner.Run("", "age-keygen", "-y", keyPath)
	if err != nil {
		return res, secretErr(70, "deriving recipient failed: %v", err)
	}
	recipient = strings.TrimSpace(recipient)

	rule := sopsconfig.Rule{
		PathRegex: fmt.Sprintf(`secrets/%s\.yaml$`, regexEscape(server)),
		Age:       recipient,
	}
	sopsPath := filepath.Join(env.RepoRoot, ".sops.yaml")
	if err := sopsconfig.UpsertRule(sopsPath, rule); err != nil {
		return res, secretErr(70, "updating .sops.yaml: %v", err)
	}

	return InitResult{
		Recipient:   recipient,
		KeyPath:     keyRel,
		SopsConfig:  ".sops.yaml",
		SecretsFile: filepath.Join("secrets", server+".yaml"),
	}, nil
}

// SyncKeysFile re-encrypts an existing secrets file to the recipients now
// declared in .sops.yaml, using the server's own age key material (passed as
// SOPS_AGE_KEY). Used after a recipient change.
func SyncKeysFile(env SecretEnv, secretsRel string, ageKeyEnv map[string]string) error {
	_, err := env.Runner.RunEnv(env.RepoRoot, ageKeyEnv, "sops", "updatekeys", "-y", secretsRel)
	if err != nil {
		return secretErr(70, "sops updatekeys %s: %v", secretsRel, err)
	}
	return nil
}

// regexEscape escapes the dots in a server name for the path_regex.
func regexEscape(s string) string {
	return strings.ReplaceAll(s, ".", `\.`)
}
