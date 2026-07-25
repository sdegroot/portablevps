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
		"run", NixpkgsFlake+"#nixos-rebuild", "--", action,
		"--flake", env.RepoRoot+"#"+server,
		"--target-host", "admin@"+host,
		"--build-host", "admin@"+host,
		"--elevate=sudo", "--no-reexec")
	if err != nil {
		// A dry run changes nothing, so there is no "did it actually apply?"
		// question to answer — any error is just a failure.
		if env.DryRun {
			report(action, "fail", err.Error())
			return deployErr(70, "nixos-rebuild %s failed: %v", action, err)
		}
		// nixos-rebuild reports failure (switch-to-configuration exit 4) when the
		// switch fully APPLIED but some unit is left in a failed state. On this
		// fleet that is almost always a benign podman healthcheck false-positive:
		// when a container restarts during the switch, podman's transient
		// healthcheck unit fires while the container is still inside its
		// HealthStartPeriod and `podman healthcheck run` exits 1 (a podman bug
		// fixed only in v6 — the start period suppresses the container KILL but
		// not the process exit code), leaving a failed <id>-<hash>.service that
		// switch-to-configuration then counts against an otherwise-good switch.
		//
		// Distinguish that from a real failure by VERIFYING container health, not
		// by trusting the exit code: benign only if every failed unit is a podman
		// healthcheck transient whose container converges to healthy. Anything
		// else — a real unit that failed to start, a container that stays
		// unhealthy, an unreachable host — falls through to the failure path.
		if units, detail := benignHealthcheckFailure(env, host); units != "" {
			report(action, "warn", "switch applied; ignoring known podman healthcheck false-positive ("+detail+")")
			// Clear the stale failed transient units so they neither linger in
			// `systemctl --failed` nor re-trigger DeployFailing alerting. (podman
			// also clears them on the container's next restart; this just doesn't
			// wait for that.)
			_, _ = env.Host.Run(host, "sudo systemctl reset-failed "+units+" || true")
			reportDeployOutcome(env, host, "success")
			report(action, "ok", host+" now runs .#"+server+" (with a tolerated healthcheck false-positive)")
			return nil
		}
		reportDeployOutcome(env, host, "failure")
		// A failed blue-green flip fails the switch; surface WHY (the
		// reconcile oneshot's log) instead of just a generic non-zero exit.
		if hint := blueGreenFailureHint(env, host); hint != "" {
			report(action, "info", hint)
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

// benignSwitchProbe inspects the host's failed units and decides whether the
// switch's non-zero exit is only the known podman healthcheck false-positive.
// It emits diagnostic lines then a final `verdict=benign units=<names>` (when
// EVERY failed unit is a podman healthcheck transient whose container reaches
// healthy) or `verdict=real reason=...`. It waits out a still-"starting"
// container (bounded) rather than mistaking a slow boot for a real failure —
// only the true-positive path polls, so a genuine failure returns promptly.
const benignSwitchProbe = `sudo bash -c '
set -u
failed=$(systemctl list-units --type=service --state=failed --plain --no-legend 2>/dev/null | awk "{print \$1}")
if [ -z "$failed" ]; then echo "verdict=real reason=no-failed-units"; exit 0; fi
benign=""
for u in $failed; do
  es=$(systemctl show -p ExecStart --value "$u" 2>/dev/null)
  case "$es" in
    *"healthcheck run"*) : ;;
    *) echo "verdict=real reason=non-healthcheck-unit unit=$u"; exit 0 ;;
  esac
  # The container id is the last long-hex token in ExecStart ("... podman
  # healthcheck run <id>"). tail -n1 is load-bearing: the podman store path can
  # itself contain short hex runs, but the id is always last, so take the last.
  cid=$(printf "%s" "$es" | grep -oE "[0-9a-f]{12,64}" | tail -n1)
  if [ -z "$cid" ]; then echo "verdict=real reason=no-container-id unit=$u"; exit 0; fi
  h=starting
  for _ in $(seq 1 30); do
    h=$(podman inspect "$cid" --format "{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}" 2>/dev/null || echo gone)
    [ "$h" = starting ] || break
    sleep 5
  done
  if [ "$h" != healthy ]; then echo "verdict=real reason=container-not-healthy unit=$u cid=$cid health=$h"; exit 0; fi
  echo "healthcheck-transient-ok unit=$u cid=$cid"
  benign="$benign $u"
done
echo "verdict=benign units=$benign"
'`

// benignHealthcheckFailure runs benignSwitchProbe and, when the failed switch is
// only the tolerated podman healthcheck false-positive, returns the failed
// transient unit names (space-separated, for `systemctl reset-failed`) plus a
// short human detail. Returns "" for units on a genuine failure or if the host
// cannot be probed (can't verify => treat as real).
func benignHealthcheckFailure(env DeployEnv, host string) (units, detail string) {
	out, err := env.Host.Run(host, benignSwitchProbe)
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "verdict=benign units="); ok {
			u := strings.TrimSpace(rest)
			if u == "" {
				return "", ""
			}
			return u, "restarted container healthy; failed unit(s): " + u
		}
		if strings.HasPrefix(line, "verdict=real") {
			return "", ""
		}
	}
	return "", ""
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
