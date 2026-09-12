package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sdegroot/portablevps/internal/sopsconfig"
)

// CrossHostSecrets re-encrypts a server's tracked secrets file so a
// DIFFERENT host's own already-registered age recipient can decrypt it,
// without ever installing that identity's private key onto the other host.
// Used by RestoreDrill when temporarily switching a spare into a source
// identity it was never provisioned for — service migrate/restore never need
// this, since their target is documented to already carry its own matching
// secrets (see docs/operations-runbooks.md "Service Migration").
type CrossHostSecrets interface {
	// Swap re-encrypts identity's tracked secrets file to hostServer's own
	// already-registered recipient (looked up from .sops.yaml — no secret
	// material needed for that lookup) and overwrites the tracked file so
	// the next local build embeds it. identity's private key, resolved via
	// ageEnv, is used only here, locally, to decrypt — it never leaves this
	// process. The returned restore func puts the original bytes back; call
	// it exactly once. A nil restore func with a nil error means identity
	// has no secrets file — nothing to swap.
	Swap(identity, hostServer string, ageEnv map[string]string) (restore func() error, err error)
}

// RepoSecrets is the default CrossHostSecrets, operating on the consumer
// repo's checked-in secrets/*.yaml and .sops.yaml via the local sops/age
// toolchain.
type RepoSecrets struct {
	RepoRoot string
	Runner   EnvRunner
}

func (s RepoSecrets) secretsPath(identity string) string {
	return filepath.Join(s.RepoRoot, "secrets", identity+".yaml")
}

// recipient looks up hostServer's own age recipient from .sops.yaml — public
// data, no secret material needed.
func (s RepoSecrets) recipient(hostServer string) (string, error) {
	rules, err := sopsconfig.Rules(filepath.Join(s.RepoRoot, ".sops.yaml"))
	if err != nil {
		return "", fmt.Errorf("reading .sops.yaml: %w", err)
	}
	pattern := fmt.Sprintf(`secrets/%s\.yaml$`, regexEscape(hostServer))
	for _, r := range rules {
		if r.PathRegex == pattern && r.Age != "" {
			return r.Age, nil
		}
	}
	return "", fmt.Errorf("no .sops.yaml recipient registered for %s (run `secret init %s` first)", hostServer, hostServer)
}

// reencrypt decrypts identity's tracked secrets file (using ageEnv) and
// re-encrypts the plaintext to recipient, returning the new ciphertext.
// sops needs a real file (not a pipe) to infer format on encryption, so the
// plaintext is staged in a private temp file, removed immediately after.
func (s RepoSecrets) reencrypt(identity, recipient string, ageEnv map[string]string) ([]byte, error) {
	rel := filepath.Join("secrets", identity+".yaml")
	plaintext, err := s.Runner.RunEnv(s.RepoRoot, ageEnv, "sops", "-d", rel)
	if err != nil {
		return nil, fmt.Errorf("decrypting %s: %w", rel, err)
	}
	tmp, err := os.CreateTemp("", "portablevps-dr-*.yaml")
	if err != nil {
		return nil, fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	_, werr := tmp.WriteString(plaintext)
	cerr := tmp.Close()
	if werr != nil {
		return nil, fmt.Errorf("writing temp plaintext: %w", werr)
	}
	if cerr != nil {
		return nil, fmt.Errorf("closing temp plaintext: %w", cerr)
	}
	// --config /dev/null: this is a one-off recipient, not identity's normal
	// creation rule — sops must not try to match tmpPath (an absolute path
	// outside the repo) against .sops.yaml's path_regex rules.
	out, err := s.Runner.Run(s.RepoRoot, "sops", "--config", os.DevNull, "--encrypt", "--age", recipient, tmpPath)
	if err != nil {
		return nil, fmt.Errorf("encrypting to %s: %w", recipient, err)
	}
	return []byte(out), nil
}

// Swap implements CrossHostSecrets.
func (s RepoSecrets) Swap(identity, hostServer string, ageEnv map[string]string) (func() error, error) {
	path := s.secretsPath(identity)
	if !fileExists(path) {
		return nil, nil
	}
	rel := filepath.Join("secrets", identity+".yaml")
	// A crash between the write below and the deferred revert must be
	// recoverable with a plain `git checkout -- <rel>` — only true if the
	// file was already clean going in.
	status, err := s.Runner.Run(s.RepoRoot, "git", "status", "--porcelain", "--", rel)
	if err != nil {
		return nil, fmt.Errorf("checking %s is git-clean: %w", rel, err)
	}
	if strings.TrimSpace(status) != "" {
		return nil, fmt.Errorf("%s has uncommitted changes; commit or stash before a cross-host drill so a crash mid-drill stays recoverable with `git checkout -- %s`", rel, rel)
	}
	recipient, err := s.recipient(hostServer)
	if err != nil {
		return nil, err
	}
	original, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	reenc, err := s.reencrypt(identity, recipient, ageEnv)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, reenc, 0o644); err != nil {
		return nil, fmt.Errorf("writing %s: %w", path, err)
	}
	restored := false
	return func() error {
		if restored {
			return nil
		}
		restored = true
		return os.WriteFile(path, original, 0o644)
	}, nil
}
