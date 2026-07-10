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
	RepoRoot        string
	Runner          EnvRunner
	OperatorAgeKey  string // e.g. .local/sops/age-key.txt (relative to RepoRoot ok)
	OperatorPubPath string // e.g. keys/operator-age.pub (relative to RepoRoot ok)
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
	Rekeyed     bool   // whether an existing secrets file was re-encrypted
}

// PerServerKeyRel is the repo-relative path of a server's age key.
func PerServerKeyRel(server string) string {
	return filepath.Join(".local", "sops", "servers", server, "age-key.txt")
}

// SecretInit runs the full ceremony transactionally: generate the per-server age
// key, merge a creation_rule into .sops.yaml (operator + per-server recipient),
// and re-key an existing secrets file. It never asks the operator to hand-edit
// .sops.yaml.
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

	operator, err := operatorRecipient(env)
	if err != nil {
		return res, err
	}

	rule := sopsconfig.Rule{
		PathRegex: fmt.Sprintf(`secrets/%s\.yaml$`, regexEscape(server)),
		Age:       operator + "," + recipient,
	}
	sopsPath := filepath.Join(env.RepoRoot, ".sops.yaml")
	if err := sopsconfig.UpsertRule(sopsPath, rule); err != nil {
		return res, secretErr(70, "updating .sops.yaml: %v", err)
	}

	res = InitResult{
		Recipient:   recipient,
		KeyPath:     keyRel,
		SopsConfig:  ".sops.yaml",
		SecretsFile: filepath.Join("secrets", server+".yaml"),
	}

	secretsPath := filepath.Join(env.RepoRoot, res.SecretsFile)
	if fileExists(secretsPath) {
		if err := SyncKeysFile(env, res.SecretsFile); err != nil {
			return res, err
		}
		res.Rekeyed = true
	}
	return res, nil
}

// SyncKeysFile re-encrypts an existing secrets file to the recipients now
// declared in .sops.yaml (used after adding an operator or rotating a key).
func SyncKeysFile(env SecretEnv, secretsRel string) error {
	opKey := filepath.Join(env.RepoRoot, opKeyRel(env))
	_, err := env.Runner.RunEnv(env.RepoRoot,
		map[string]string{"SOPS_AGE_KEY_FILE": opKey},
		"sops", "updatekeys", "-y", secretsRel)
	if err != nil {
		return secretErr(70, "sops updatekeys %s: %v", secretsRel, err)
	}
	return nil
}

// operatorRecipient returns the operator's age recipient: the committed
// keys/operator-age.pub if present, otherwise derived from the operator age key.
func operatorRecipient(env SecretEnv) (string, error) {
	pubRel := env.OperatorPubPath
	if pubRel == "" {
		pubRel = filepath.Join("keys", "operator-age.pub")
	}
	pubPath := filepath.Join(env.RepoRoot, pubRel)
	if data, err := os.ReadFile(pubPath); err == nil {
		return strings.TrimSpace(string(data)), nil
	}
	// Derive from the operator age key.
	opKey := filepath.Join(env.RepoRoot, opKeyRel(env))
	if !fileExists(opKey) {
		return "", secretErr(66, "operator recipient not found: neither %s nor the operator age key %s exists (run `secret keygen`)", pubRel, opKeyRel(env))
	}
	recipient, err := env.Runner.Run("", "age-keygen", "-y", opKey)
	if err != nil {
		return "", secretErr(70, "deriving operator recipient: %v", err)
	}
	return strings.TrimSpace(recipient), nil
}

func opKeyRel(env SecretEnv) string {
	if env.OperatorAgeKey != "" {
		return env.OperatorAgeKey
	}
	return filepath.Join(".local", "sops", "age-key.txt")
}

// regexEscape escapes the dots in a server name for the path_regex.
func regexEscape(s string) string {
	return strings.ReplaceAll(s, ".", `\.`)
}
