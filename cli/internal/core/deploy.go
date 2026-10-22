package core

import (
	"fmt"
	"strings"
	"time"
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
	// BackupFirst runs the host's backup (as `pvps backup run`) before the
	// switch and deploys nothing if it fails.
	BackupFirst bool
	Sleep       func(time.Duration) // injectable for tests; nil = time.Sleep
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
// reachability check, optionally a backup, then `nixos-rebuild switch` built on
// the remote (so a different-arch operator can drive an x86_64 host), then a
// best-effort deploy outcome report. No identity or data changes (unlike
// repurpose).
func Deploy(env DeployEnv, server, host string) error {
	report := env.Report
	if report == nil {
		report = func(string, string, string) {}
	}
	sleep := env.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	if env.BackupFirst && env.DryRun {
		return deployErr(64, "--backup-first takes a real backup, so it cannot be combined with --dry-run")
	}

	report("preflight", "run", "checking admin SSH to "+host)
	if _, err := env.Host.Run(host, "true"); err != nil {
		return deployErr(71, "admin SSH to %s failed: %v", host, err)
	}
	report("preflight", "ok", "")

	if env.BackupFirst {
		if err := backupBeforeDeploy(env.Host, host, report); err != nil {
			return err
		}
	}

	action := "switch"
	since := ""
	if env.DryRun {
		action = "dry-activate"
	} else {
		since = hostEpoch(env.Host, host)
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
	if err == nil {
		if env.DryRun {
			report(action, "ok", "dry run complete; no changes made")
			return nil
		}
		reportDeployOutcome(env, host, "success")
		report(action, "ok", host+" now runs .#"+server)
		return nil
	}
	// A dry run changes nothing, so there is no "did it actually apply?"
	// question to answer — any error is just a failure.
	if env.DryRun {
		report(action, "fail", err.Error())
		return deployErr(70, "nixos-rebuild %s failed: %v", action, err)
	}
	return judgeFailedSwitch(env, server, host, since, err, report, sleep)
}

// judgeFailedSwitch decides a non-zero `nixos-rebuild switch` from the host's
// own record rather than the exit code, which conflates three very different
// things:
//
//   - The switch never started (evaluation, build or copy failed): a failure.
//   - The SSH session dropped mid-switch (exit 255, typically because the switch
//     restarted the mesh client the session runs over) while the host went on
//     to finish it: a success, once the journal and /run/current-system say so.
//   - The switch applied but left a unit failed (switch-to-configuration exit
//     4). On this fleet that is almost always transient: a podman healthcheck
//     false-positive (see failedUnitsProbe), or a unit that failed once during
//     the switch and recovered through its own Restart= policy. Tolerated only
//     when the host verifiably converged: the target generation is current
//     and either every failed unit is a healthcheck transient whose container
//     went healthy, or nothing is failed and the host stays cleanly "running"
//     — no failed units, nothing in auto-restart — across several samples.
//     Anything else (a unit that stays failed, a crash loop, a degraded host,
//     an activation-script error) is a failure.
func judgeFailedSwitch(env DeployEnv, server, host, since string, switchErr error, report func(phase, status, msg string), sleep func(time.Duration)) error {
	const action = "switch"
	fail := func(format string, a ...any) error {
		reportDeployOutcome(env, host, "failure")
		// A failed blue-green flip fails the switch; surface WHY (the
		// reconcile oneshot's log) instead of just a generic non-zero exit.
		if hint := blueGreenFailureHint(env, host); hint != "" {
			report(action, "info", hint)
		}
		msg := fmt.Sprintf(format, a...)
		report(action, "fail", msg)
		return deployErr(70, "%s", msg)
	}
	pass := func(warning string) error {
		report(action, "warn", warning)
		reportDeployOutcome(env, host, "success")
		report(action, "ok", host+" now runs .#"+server)
		return nil
	}

	report(action, "info", fmt.Sprintf("nixos-rebuild exited with %v; checking the switch on %s itself", switchErr, host))
	st, reachable := awaitSwitch(env.Host, host, since, sleep)
	if !reachable {
		report(action, "fail", "admin SSH did not come back, so the switch cannot be verified")
		return deployErr(70, "nixos-rebuild switch failed (%v) and %s did not come back over admin SSH to verify it; "+
			"check `journalctl -u %s` and /run/current-system on the host", switchErr, host, switchUnit)
	}
	switch {
	case st.Target == "":
		return fail("nixos-rebuild switch failed before activating anything: %v", switchErr)
	case !st.Done && st.Running:
		return fail("the switch to %s is still running on %s; check `journalctl -u %s`", st.Target, host, switchUnit)
	case !st.Done:
		return fail("the switch to %s on %s ended without an outcome (killed?); check `journalctl -u %s`", st.Target, host, switchUnit)
	}
	if cur := currentSystem(env.Host, host); cur != st.Target {
		return fail("%s runs %q, not the generation just switched to (%s); another switch (autoUpgrade?) may have run",
			host, cur, st.Target)
	}
	switch st.Status {
	case 0:
		return pass("the SSH session dropped before nixos-rebuild could confirm (" + switchErr.Error() +
			"), but the host finished the switch")
	case 4:
		v := probeFailedUnits(env.Host, host)
		if v.Benign {
			clearHealthcheckTransients(env.Host, host, v.Units)
			return pass("switch applied; ignoring known podman healthcheck false-positive (restarted container healthy; failed unit(s): " + v.Units + ")")
		}
		if v.Reason != "no-failed-units" {
			reason := v.Reason
			if reason == "" {
				reason = "the failed units could not be checked"
			}
			return fail("switch applied but units failed (%s): %v", reason, switchErr)
		}
		ok, why := settledHealthy(env.Host, host, sleep)
		if !ok {
			return fail("switch applied with failed units and the host has not recovered (%s)", why)
		}
		recovered := unitsFailedDuringSwitch(env.Host, host, since)
		if recovered == "" {
			recovered = "see the journal"
		}
		return pass("switch-to-configuration exited 4, but every unit has since recovered and the host stays cleanly running (failed during the switch: " + recovered + ")")
	default:
		return fail("switch-to-configuration on %s exited with status %d: %v", host, st.Status, switchErr)
	}
}

// backupBeforeDeploy runs the host's own backup service and refuses to go on
// unless it succeeds. A host without portablevps-backup.service is an error
// rather than a silent skip: the operator asked for a backup and would
// otherwise believe there is one.
func backupBeforeDeploy(h HostRunner, host string, report func(phase, status, msg string)) error {
	out, err := h.Run(host, "systemctl cat portablevps-backup.service >/dev/null 2>&1 && echo present || echo absent")
	if err != nil {
		return deployErr(71, "checking for a backup service on %s failed: %v", host, err)
	}
	if strings.TrimSpace(out) != "present" {
		report("backup", "fail", "no portablevps-backup.service on "+host)
		return deployErr(78, "%s has no portablevps-backup.service, so --backup-first cannot take a backup; "+
			"enable backups for this server or deploy without --backup-first", host)
	}
	if err := RunBackup(BackupEnv{Host: h, Report: report}, host); err != nil {
		return deployErr(70, "backup before deploy failed, so nothing was deployed: %v", err)
	}
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
