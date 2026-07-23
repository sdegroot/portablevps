// Package keystore resolves per-server key material through a fixed order:
// 1Password → file fallback → error. It backs the "Model A" design: each server
// has one 1Password item holding its admin SSH key and its age key, with no
// operator master key. Interactive operators use the 1Password SSH agent (the
// private key never touches disk); CI uses a service-account `op read` to a
// shredded temp file; a file fallback keeps a 1Password outage from locking the
// operator out.
package keystore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Runner runs the `op` CLI. The exec adapter satisfies it; tests inject a fake.
type Runner interface {
	Run(dir, name string, args ...string) (string, error)
}

// Mode selects how an SSH key is presented.
type Mode int

const (
	// Interactive uses the 1Password SSH agent (key never on disk).
	Interactive Mode = iota
	// Headless resolves the key to a shredded temp file (CI / no agent).
	Headless
)

// Ref identifies a server's keys: a 1Password item and a file fallback.
type Ref struct {
	OpItem   string // e.g. "op://Epistola/web" (the item; fields are /age-key, /"private key")
	FilePath string // fallback private-key file (e.g. .local/ssh/web_ed25519 or .local/sops/servers/web/age-key.txt)
	PubPath  string // public key file (non-secret) used to select the agent identity
}

// Store resolves references.
type Store struct {
	Runner    Runner
	OpAccount string // 1Password account for `op`
}

// ErrNoKey means neither 1Password nor a file fallback yielded the key.
var ErrNoKey = errors.New("key not found in 1Password or a file fallback")

// AgeMaterial returns the age private key content for SOPS_AGE_KEY: the item's
// age-key field, else the fallback file's content.
func (s Store) AgeMaterial(ref Ref) (string, error) {
	if ref.OpItem != "" {
		if out, err := s.opRead(ref.OpItem + "/age-key"); err == nil {
			return strings.TrimSpace(out), nil
		} else if ref.FilePath == "" {
			return "", fmt.Errorf("reading age key from 1Password (%s): %w", ref.OpItem, err)
		}
	}
	if ref.FilePath != "" {
		data, err := os.ReadFile(ref.FilePath)
		if err != nil {
			return "", fmt.Errorf("reading age key file %s: %w", ref.FilePath, err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	return "", ErrNoKey
}

// SSHIdentity returns ssh -o options for authenticating with the server's admin
// key, plus a cleanup func (shreds a temp key in Headless mode; no-op otherwise).
func (s Store) SSHIdentity(ref Ref, mode Mode) (opts []string, cleanup func(), err error) {
	noop := func() {}

	// Interactive operators prefer the local break-glass key when present. This
	// avoids relying on OpenSSH's public-key IdentityFile agent-selection edge.
	if mode == Interactive && ref.FilePath != "" {
		if _, statErr := os.Stat(ref.FilePath); statErr == nil {
			return []string{"-i", ref.FilePath, "-o", "IdentitiesOnly=yes"}, noop, nil
		}
	}

	// With a 1Password item: op read the private key to a shredded temp.
	if ref.OpItem != "" {
		key, readErr := s.opRead(ref.OpItem + "/private key")
		if readErr == nil {
			tmp, err := writeTempKey(key)
			if err != nil {
				return nil, noop, err
			}
			return []string{"-i", tmp, "-o", "IdentitiesOnly=yes"},
				func() { shred(tmp) }, nil
		}
		if ref.FilePath == "" {
			return nil, noop, fmt.Errorf("reading SSH key from 1Password (%s): %w", ref.OpItem, readErr)
		}
		// fall through to the file fallback
	}

	// File fallback (break-glass / offline).
	if ref.FilePath != "" {
		return []string{"-i", ref.FilePath, "-o", "IdentitiesOnly=yes"}, noop, nil
	}
	return nil, noop, ErrNoKey
}

func (s Store) opRead(ref string) (string, error) {
	args := []string{"read"}
	if s.OpAccount != "" {
		args = append(args, "--account", s.OpAccount)
	}
	args = append(args, ref)
	return s.Runner.Run("", "op", args...)
}

func writeTempKey(content string) (string, error) {
	f, err := os.CreateTemp("", "portablevps-key-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if err := os.Chmod(name, 0o600); err != nil {
		f.Close()
		return "", err
	}
	if _, err := f.WriteString(strings.TrimRight(content, "\n") + "\n"); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return name, nil
}

// shred overwrites and removes a temp key file (best effort).
func shred(path string) {
	if info, err := os.Stat(path); err == nil {
		_ = os.WriteFile(path, make([]byte, info.Size()), 0o600)
	}
	_ = os.Remove(path)
}

// DefaultOpItem is the conventional 1Password item ref for a server, under vault.
func DefaultOpItem(vault, server string) string {
	if vault == "" {
		return ""
	}
	return filepath.ToSlash("op://" + vault + "/" + server)
}
