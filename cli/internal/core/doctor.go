package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Status is the outcome of a single health check.
type Status string

const (
	StatusOK   Status = "ok"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
)

// Check is one line of the doctor report.
type Check struct {
	Status Status `json:"status"`
	Title  string `json:"title"`
	Hint   string `json:"hint,omitempty"`
}

// Env is everything the doctor (and other core operations) need from the outside
// world: where the consumer repo is, how to run commands, how to look commands
// up on PATH, and how to read the process environment. Injecting these keeps
// core pure and unit-testable, and lets CI drive it deterministically.
type Env struct {
	RepoRoot   string
	Runner     CommandRunner
	HasCommand func(name string) bool
	Getenv     func(name string) string
}

// RunDoctor returns the environment/repository health checks, optionally
// including per-server checks when serverName is non-empty.
func RunDoctor(env Env, serverName string) []Check {
	var checks []Check
	add := func(status Status, title, hint string) {
		checks = append(checks, Check{Status: status, Title: title, Hint: hint})
	}

	// 1. Required tooling.
	for _, tool := range []string{"nix", "git", "ssh"} {
		if env.HasCommand(tool) {
			add(StatusOK, tool+" available", "")
		} else {
			add(StatusFail, tool+" not found", "install "+tool+" and re-run")
		}
	}

	// 2. Consumer repository layout. Walk up so a consumer that is a subdirectory
	// of a git root (a monorepo layout) is still recognised as tracked.
	if inGitTree(env.RepoRoot) {
		add(StatusOK, "consumer repo is a git checkout", "")
	} else {
		add(StatusWarn, "not a git repository",
			"Nix flakes only see git-tracked files; run inside your consumer repo and `git add` new files")
	}
	if fileExists(filepath.Join(env.RepoRoot, "flake.nix")) {
		add(StatusOK, "flake.nix present", "")
	} else {
		add(StatusFail, "flake.nix missing",
			fmt.Sprintf("run doctor from your consumer repo root (looked in %s)", env.RepoRoot))
	}

	// 3. Flake evaluates (implicitly validates the servers/ definitions).
	servers, err := LoadServers(env)
	switch {
	case err != nil:
		add(StatusFail, "flake does not evaluate", firstLine(err.Error()))
	case len(servers) == 0:
		add(StatusWarn, "flake evaluates but declares no servers", "add servers/<name>.nix (see the template)")
	default:
		add(StatusOK, fmt.Sprintf("flake evaluates (%d server(s): %s)",
			len(servers), strings.Join(SortedNames(servers), ", ")), "")
	}

	// 4. Operator control-plane keys.
	adminKey := envOr(env.Getenv, "CLOUD_ADMIN_KEY", ".local/ssh/cloud-admin_ed25519")
	adminKeyPath := repoPath(env.RepoRoot, adminKey)
	if info, statErr := os.Stat(adminKeyPath); statErr == nil {
		if info.Mode().Perm()&0o077 != 0 {
			add(StatusWarn, "cloud admin SSH key is group/world-accessible", "chmod 600 "+adminKeyPath)
		} else {
			add(StatusOK, "cloud admin SSH key present", "")
		}
	} else {
		add(StatusWarn, "cloud admin SSH key missing", "generate it (keygen) at "+adminKeyPath)
	}
	adminPub := repoPath(env.RepoRoot, envOr(env.Getenv, "CLOUD_ADMIN_PUBKEY", "keys/cloud-admin.pub"))
	if fileExists(adminPub) {
		add(StatusOK, "cloud admin public key present", "")
	} else {
		add(StatusWarn, "cloud admin public key missing", adminPub)
	}

	// 5. Server-specific checks.
	if serverName != "" {
		server, ok := servers[serverName]
		if !ok {
			add(StatusFail, fmt.Sprintf("server %q not found", serverName),
				"known servers: "+strings.Join(SortedNames(servers), ", "))
			return checks
		}

		if server.Provider != "" {
			add(StatusOK, fmt.Sprintf("provider %q declared", server.Provider), "")
		} else {
			add(StatusFail, "server declares no provider", "set placement.provider in the server definition")
		}

		if server.BackupRepository != "" {
			add(StatusOK, "backupRepository set", "")
		} else {
			add(StatusWarn, "no backupRepository declared",
				"set portablevps.server.backupRepository if this server backs up")
		}

		age := repoPath(env.RepoRoot, resolveAgeKey(env.Getenv, env.RepoRoot, serverName))
		if fileExists(age) {
			add(StatusOK, "host age key present", "")
		} else {
			add(StatusWarn, "host age key missing", "run secrets-init-server for "+serverName)
		}

		secretsFile := filepath.Join(env.RepoRoot, "secrets", serverName+".yaml")
		if fileExists(secretsFile) {
			add(StatusOK, "secrets file present", "")
		} else {
			add(StatusWarn, "secrets file missing (by convention)", secretsFile)
		}
	}

	return checks
}

// HasFailures reports whether any check failed.
func HasFailures(checks []Check) bool {
	for _, c := range checks {
		if c.Status == StatusFail {
			return true
		}
	}
	return false
}

// Counts returns the number of failures and warnings.
func Counts(checks []Check) (fails, warns int) {
	for _, c := range checks {
		switch c.Status {
		case StatusFail:
			fails++
		case StatusWarn:
			warns++
		}
	}
	return fails, warns
}

func resolveAgeKey(getenv func(string) string, repoRoot, serverName string) string {
	if explicit := getenv("SOPS_AGE_KEY_FILE"); explicit != "" {
		return explicit
	}
	perServer := filepath.Join(".local", "sops", "servers", serverName, "age-key.txt")
	if fileExists(repoPath(repoRoot, perServer)) {
		return perServer
	}
	return filepath.Join(".local", "sops", "age-key.txt")
}

func inGitTree(dir string) bool {
	for {
		if fileExists(filepath.Join(dir, ".git")) {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

func repoPath(repoRoot, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(repoRoot, p)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func envOr(getenv func(string) string, name, fallback string) string {
	if v := getenv(name); v != "" {
		return v
	}
	return fallback
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
