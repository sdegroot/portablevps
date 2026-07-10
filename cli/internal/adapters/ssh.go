package adapters

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SSH drives an already-installed host over the admin account, mirroring the
// Python CLI's admin_ssh/wait_admin_ssh. Host-key verification is TOFU
// (accept-new pinned into an operator-local known_hosts under .local), so a
// later MITM — including the session that ships a host's age key — is detected.
type SSH struct {
	RepoRoot     string
	AdminKey     string // key path (absolute, or relative to RepoRoot)
	Port         string // default "22"
	WaitAttempts int    // default 120
	WaitDelay    time.Duration
	sleep        func(time.Duration) // injectable for tests
}

func (s SSH) port() string {
	if s.Port == "" {
		return "22"
	}
	return s.Port
}

func (s SSH) keyPath() string {
	if filepath.IsAbs(s.AdminKey) {
		return s.AdminKey
	}
	return filepath.Join(s.RepoRoot, s.AdminKey)
}

// knownHosts returns the operator-local known_hosts path, creating its dir.
func (s SSH) knownHosts() string {
	p := filepath.Join(s.RepoRoot, ".local", "known_hosts")
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	return p
}

func (s SSH) baseArgs(host string) []string {
	return []string{
		"-p", s.port(),
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=" + s.knownHosts(),
		"-i", s.keyPath(),
		"-o", "IdentitiesOnly=yes",
		"admin@" + host,
	}
}

// Run executes command on host over admin SSH and returns trimmed stdout.
func (s SSH) Run(host, command string) (string, error) {
	args := append(s.baseArgs(host), command)
	return ExecRunner{}.Run("", "ssh", args...)
}

// WaitReady polls until admin SSH succeeds or the attempt budget is exhausted.
func (s SSH) WaitReady(host string) error {
	attempts := s.WaitAttempts
	if attempts == 0 {
		attempts = 120
	}
	delay := s.WaitDelay
	if delay == 0 {
		delay = 5 * time.Second
	}
	sleep := s.sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	for i := 1; i <= attempts; i++ {
		if _, err := s.Run(host, "true"); err == nil {
			return nil
		}
		if i == attempts {
			return fmt.Errorf("admin SSH did not become ready on %s", host)
		}
		sleep(delay)
	}
	return nil
}

// NixSSHOpts returns the value for NIX_SSHOPTS so nixos-rebuild's
// --target-host/--build-host use the same key and TOFU verification.
func (s SSH) NixSSHOpts() string {
	return fmt.Sprintf("-i %s -o IdentitiesOnly=yes -p %s -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=%s",
		s.keyPath(), s.port(), s.knownHosts())
}
