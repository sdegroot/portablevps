package core

import (
	"fmt"
	"strings"
)

// HostRunner drives an already-installed host over admin SSH. adapters.SSH
// satisfies it; tests inject a fake.
type HostRunner interface {
	Run(host, command string) (string, error)
	RunInput(host, command, input string) (string, error)
	WaitReady(host string) error
	NixSSHOpts() string
}

// StreamRunner runs a local command with its output piped live to the terminal.
type StreamRunner interface {
	Stream(dir string, env map[string]string, name string, args ...string) error
}

// DeployEnv is the injected environment for an in-place deploy.
type DeployEnv struct {
	RepoRoot string
	Runner   StreamRunner // local commands (nixos-rebuild), streamed live
	Host     HostRunner   // admin SSH to the target
	Report   func(phase, status, msg string)
	DryRun   bool // build + show what would change, but do not switch
}

// DeployError carries a sysexits-style exit code.
type DeployError struct {
	Code int
	Msg  string
}

func (e *DeployError) Error() string { return e.Msg }
func (e *DeployError) ExitCode() int { return e.Code }
func deployErr(code int, format string, a ...any) *DeployError {
	return &DeployError{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// Deploy pushes the committed config to a running host in place: an admin-SSH
// reachability check, then `nixos-rebuild switch` built on the remote (so a
// different-arch operator can drive an x86_64 host), then a best-effort deploy
// outcome report. No identity or data changes (unlike repurpose).
func Deploy(env DeployEnv, server, host string) error {
	report := env.Report
	if report == nil {
		report = func(string, string, string) {}
	}

	report("preflight", "run", "checking admin SSH to "+host)
	if _, err := env.Host.Run(host, "true"); err != nil {
		return deployErr(71, "admin SSH to %s failed: %v", host, err)
	}
	report("preflight", "ok", "")

	action := "switch"
	if env.DryRun {
		action = "dry-activate"
	}
	report(action, "run", fmt.Sprintf("nixos-rebuild %s .#%s on %s (built on the remote)", action, server, host))
	err := env.Runner.Stream(env.RepoRoot,
		map[string]string{"NIX_SSHOPTS": env.Host.NixSSHOpts()},
		"nix", "--extra-experimental-features", "nix-command flakes",
		"run", "nixpkgs#nixos-rebuild", "--", action,
		"--flake", env.RepoRoot+"#"+server,
		"--target-host", "admin@"+host,
		"--build-host", "admin@"+host,
		"--elevate=sudo", "--no-reexec")
	if err != nil {
		if !env.DryRun {
			reportDeployOutcome(env, host, "failure")
			// A failed blue-green flip fails the switch; surface WHY (the
			// reconcile oneshot's log) instead of just a generic non-zero exit.
			if hint := blueGreenFailureHint(env, host); hint != "" {
				report(action, "info", hint)
			}
		}
		report(action, "fail", err.Error())
		return deployErr(70, "nixos-rebuild %s failed: %v", action, err)
	}
	if env.DryRun {
		report(action, "ok", "dry run complete; no changes made")
		return nil
	}
	reportDeployOutcome(env, host, "success")
	report(action, "ok", host+" now runs .#"+server)
	return nil
}

// reportDeployOutcome starts the host's own deploy-report unit (best effort), so
// DeployFailing alerting covers the operator deploy path too. Never let a
// reporting failure mask the deploy result.
func reportDeployOutcome(env DeployEnv, host, outcome string) {
	unit := "portablevps-deploy-report-" + outcome + ".service"
	_, _ = env.Host.Run(host, "sudo systemctl start "+unit+" || true")
}

// blueGreenFailureHint returns the tail of any blue-green reconcile journal so a
// switch that failed because a colour flip failed shows the cause, not just a
// generic exit code. Best effort — returns "" on any error or no blue-green app.
func blueGreenFailureHint(env DeployEnv, host string) string {
	out, err := env.Host.Run(host,
		"sudo journalctl -u '*-bluegreen.service' -n 30 --no-pager 2>/dev/null "+
			"| grep -i 'blue-green' | tail -n 8")
	if err != nil {
		return ""
	}
	if out = strings.TrimSpace(out); out == "" {
		return ""
	}
	return "blue-green reconcile log:\n" + out
}
