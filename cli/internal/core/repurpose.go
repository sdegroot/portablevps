package core

import (
	"fmt"
	"strings"
)

// RepurposeEnv drives an in-place switch of a running host to a DIFFERENT
// server config over admin SSH — no reinstall.
type RepurposeEnv struct {
	RepoRoot       string
	Host           HostRunner
	Stream         StreamRunner
	AgeKeyMaterial string // the target server's host age key, shipped to /etc/sops/age/keys.txt
	Report         func(phase, status, msg string)
}

// RepurposeOpts are the per-run parameters.
type RepurposeOpts struct {
	Server     string
	Host       string   // address
	ResetPaths []string // data dirs to clear (must be under /data or /var/lib)
}

// Repurpose switches an already-portablevps host to a different logical server
// in place: stop apps, swap the host age key to the target server's, optionally
// clear data directories the new app must own clean, then nixos-rebuild switch.
// NetBird/machine state persist (the peer keeps its address, just renamed).
func Repurpose(env RepurposeEnv, opts RepurposeOpts) error {
	report := env.Report
	if report == nil {
		report = func(string, string, string) {}
	}
	if env.AgeKeyMaterial == "" {
		return provisionErr(66, "no host age key resolved for %s", opts.Server)
	}
	for _, p := range opts.ResetPaths {
		if !strings.HasPrefix(p, "/data/") && !strings.HasPrefix(p, "/var/lib/") {
			return provisionErr(64, "refusing to reset a path outside /data or /var/lib: %s", p)
		}
	}

	report("preflight", "run", "checking admin SSH to "+opts.Host)
	if _, err := env.Host.Run(opts.Host, "true"); err != nil {
		return provisionErr(71, "admin SSH to %s failed: %v", opts.Host, err)
	}

	report("stop", "run", "stopping apps.target on "+opts.Host)
	if _, err := env.Host.Run(opts.Host, "sudo systemctl stop apps.target || true"); err != nil {
		return provisionErr(70, "stopping apps on %s: %v", opts.Host, err)
	}

	report("age-key", "run", "installing the target host age key on "+opts.Host)
	remote := "set -eu; sudo mkdir -p /etc/sops/age; sudo tee /etc/sops/age/keys.txt >/dev/null; " +
		"sudo chmod 0400 /etc/sops/age/keys.txt; sudo chown root:root /etc/sops/age/keys.txt"
	if _, err := env.Host.RunInput(opts.Host, remote, strings.TrimRight(env.AgeKeyMaterial, "\n")+"\n"); err != nil {
		return provisionErr(70, "installing age key on %s: %v", opts.Host, err)
	}

	for _, p := range opts.ResetPaths {
		report("reset", "run", "clearing "+p+" on "+opts.Host)
		cmd := fmt.Sprintf("sudo find %s -mindepth 1 -maxdepth 1 -exec rm -rf {} +", shellQuote(p))
		if _, err := env.Host.Run(opts.Host, cmd); err != nil {
			return provisionErr(70, "clearing %s on %s: %v", p, opts.Host, err)
		}
	}

	report("switch", "run", fmt.Sprintf("nixos-rebuild switch .#%s on %s (built on the remote)", opts.Server, opts.Host))
	err := env.Stream.Stream(env.RepoRoot,
		map[string]string{"NIX_SSHOPTS": env.Host.NixSSHOpts()},
		"nix", "--extra-experimental-features", "nix-command flakes",
		"run", "nixpkgs#nixos-rebuild", "--", "switch",
		"--flake", env.RepoRoot+"#"+opts.Server,
		"--target-host", "admin@"+opts.Host,
		"--build-host", "admin@"+opts.Host,
		"--elevate=sudo", "--no-reexec")
	if err != nil {
		report("switch", "fail", err.Error())
		return provisionErr(70, "nixos-rebuild switch failed: %v", err)
	}
	report("switch", "ok", opts.Host+" now runs .#"+opts.Server)
	return nil
}

// shellQuote single-quotes a shell argument.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
