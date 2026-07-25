package core

import (
	"fmt"
	"strings"
	"time"
)

const bootIDPath = "/proc/sys/kernel/random/boot_id"

// RebootEnv drives a controlled host reboot over admin SSH.
type RebootEnv struct {
	Host     HostRunner
	Report   func(phase, status, msg string)
	Attempts int
	Delay    time.Duration
	Sleep    func(time.Duration)
}

// RebootResult is returned only after the host has completed a distinct boot
// and the booted generation matches the current system profile.
type RebootResult struct {
	BootID string
	System string
}

// Reboot restarts a host and proves that it actually went through a new boot.
// A transient SSH disconnect is expected; the boot ID, not reachability alone,
// prevents an immediate pre-shutdown SSH success from becoming a false pass.
func Reboot(env RebootEnv, host string) (RebootResult, error) {
	report := env.Report
	if report == nil {
		report = func(string, string, string) {}
	}
	attempts := env.Attempts
	if attempts <= 0 {
		attempts = 90
	}
	delay := env.Delay
	if delay <= 0 {
		delay = 2 * time.Second
	}
	sleep := env.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}

	report("preflight", "run", "reading current boot identity on "+host)
	beforeBootID, err := env.Host.Run(host, "cat "+bootIDPath)
	if err != nil {
		return RebootResult{}, deployErr(71, "reading boot identity on %s: %v", host, err)
	}
	if strings.TrimSpace(beforeBootID) == "" {
		return RebootResult{}, deployErr(71, "%s returned an empty boot identity", host)
	}
	report("preflight", "ok", "")

	report("reboot", "run", "requesting system reboot on "+host)
	rebootErr := error(nil)
	if _, err := env.Host.Run(host, "sudo systemctl reboot"); err != nil {
		// ssh may lose the connection before systemctl's success reaches the
		// client. Preserve the error and let the changed boot ID decide whether
		// the reboot really happened.
		rebootErr = err
		report("reboot", "warn", "SSH closed while requesting reboot; waiting for a changed boot ID")
	}

	report("wait", "run", fmt.Sprintf("waiting for %s to complete a new boot", host))
	var afterBootID string
	for i := 1; i <= attempts; i++ {
		out, err := env.Host.Run(host, "cat "+bootIDPath)
		if err == nil {
			out = strings.TrimSpace(out)
			if out != "" && out != strings.TrimSpace(beforeBootID) {
				afterBootID = out
				break
			}
		}
		if i < attempts {
			sleep(delay)
		}
	}
	if afterBootID == "" {
		if rebootErr != nil {
			return RebootResult{}, deployErr(70, "reboot request on %s failed and no new boot was observed: %v", host, rebootErr)
		}
		return RebootResult{}, deployErr(70, "%s did not complete a new boot within the timeout", host)
	}
	report("wait", "ok", "admin SSH returned with a changed boot ID")

	report("settle", "run", "waiting for systemd to finish booting")
	var systemState string
	for i := 1; i <= attempts; i++ {
		out, err := env.Host.Run(host, "sudo systemctl is-system-running 2>/dev/null || true")
		if err == nil {
			systemState = strings.TrimSpace(out)
			if systemState == "running" || systemState == "degraded" {
				break
			}
		}
		if i < attempts {
			sleep(delay)
		}
	}
	if systemState != "running" && systemState != "degraded" {
		return RebootResult{}, deployErr(70, "%s did not finish booting within the timeout (system state %q)", host, systemState)
	}
	report("settle", "ok", systemState)

	report("verify", "run", "checking booted generation and failed units")
	booted, err := env.Host.Run(host, "readlink -f /run/booted-system")
	if err != nil {
		return RebootResult{}, deployErr(70, "reading booted generation on %s: %v", host, err)
	}
	current, err := env.Host.Run(host, "readlink -f /run/current-system")
	if err != nil {
		return RebootResult{}, deployErr(70, "reading current generation on %s: %v", host, err)
	}
	booted = strings.TrimSpace(booted)
	current = strings.TrimSpace(current)
	if booted == "" || booted != current {
		return RebootResult{}, deployErr(70, "%s booted %q but current system is %q", host, booted, current)
	}

	failed, err := env.Host.Run(host, "sudo systemctl list-units --type=service --state=failed --plain --no-legend")
	if err != nil {
		return RebootResult{}, deployErr(70, "checking failed units on %s: %v", host, err)
	}
	if failed = strings.TrimSpace(failed); failed != "" {
		return RebootResult{}, deployErr(70, "%s returned from reboot with failed services: %s", host, firstLine(failed))
	}
	report("verify", "ok", current)
	return RebootResult{BootID: afterBootID, System: current}, nil
}
