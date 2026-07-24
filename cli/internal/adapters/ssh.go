package adapters

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SSH drives an already-installed host over the admin account, mirroring the
// Python CLI's admin_ssh/wait_admin_ssh. Host-key verification is TOFU
// (accept-new pinned into an operator-local known_hosts under .local), so a
// later MITM — including the session that ships a host's age key — is detected.
// IdentityOpts carries the ssh -i/-o identity options, produced by the keystore
// (1Password agent, an op-read temp key, or a file fallback).
type SSH struct {
	RepoRoot     string
	IdentityOpts []string // ssh identity options from the keystore
	Port         string   // default "22"
	WaitAttempts int      // default 120
	WaitDelay    time.Duration
	sleep        func(time.Duration) // injectable for tests
}

func (s SSH) port() string {
	if s.Port == "" {
		return "22"
	}
	return s.Port
}

// knownHosts returns the operator-local known_hosts path, creating its dir.
func (s SSH) knownHosts() string {
	p := filepath.Join(s.RepoRoot, ".local", "known_hosts")
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	return p
}

func (s SSH) baseArgs(host string) []string {
	args := []string{
		"-p", s.port(),
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=" + s.knownHosts(),
	}
	args = append(args, s.IdentityOpts...)
	return append(args, "admin@"+host)
}

// CommandArgs returns ssh(1) arguments for an operator-facing SSH session.
func (s SSH) CommandArgs(host string, remoteCommand ...string) []string {
	args := s.baseArgs(host)
	return append(args, remoteCommand...)
}

// Run executes command on host over admin SSH and returns trimmed stdout.
func (s SSH) Run(host, command string) (string, error) {
	args := append(s.baseArgs(host), command)
	return ExecRunner{}.Run("", "ssh", args...)
}

// RunInput executes command on host over admin SSH with stdin fed from input
// (used to ship a key over `sudo tee`), returning trimmed stdout.
func (s SSH) RunInput(host, command, input string) (string, error) {
	args := append(s.baseArgs(host), command)
	cmd := exec.Command("ssh", args...)
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return "", &RunError{Err: err, Stderr: strings.TrimSpace(stderr.String())}
		}
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
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
// --target-host/--build-host use the same identity and TOFU verification.
func (s SSH) NixSSHOpts() string {
	parts := append([]string{}, s.IdentityOpts...)
	parts = append(parts, "-p", s.port(),
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile="+s.knownHosts())
	return strings.Join(parts, " ")
}
