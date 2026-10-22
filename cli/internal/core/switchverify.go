package core

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// After a failed `nixos-rebuild switch` exit, the deploy asks the host what
// actually happened instead of trusting the exit code. nixos-rebuild runs
// switch-to-configuration under systemd-run as the transient unit
// nixos-rebuild-switch-to-configuration.service (--collect), so the switch keeps
// going when the SSH session that started it drops. That is the common case on a
// mesh-only host: the switch restarts the mesh client (netbird.service), the
// session over the mesh dies, and nixos-rebuild exits 255 while the host goes on
// to finish the switch. Because the unit is garbage-collected once it ends, its
// Result is gone by the time we look (`systemctl show` then reports a
// not-loaded unit's defaults, Result=success, whatever happened) — the journal
// is the only record left. switch-to-configuration logs exactly one start line
// and one outcome line per switch:
//
//	switching to system configuration /nix/store/<toplevel>
//	finished switching to system configuration /nix/store/<toplevel>
//	switching to system configuration /nix/store/<toplevel> failed (status N)
const switchUnit = "nixos-rebuild-switch-to-configuration.service"

// Verification budget. Reconnecting uses the real admin key, so a refused or
// timed-out attempt never reaches sshd's auth (no fail2ban strike); the delay
// still keeps it far from a probe storm.
var (
	verifyDelay             = 10 * time.Second
	verifyReconnectAttempts = 18 // ≈ 3 min for admin SSH to come back
	verifySwitchAttempts    = 60 // ≈ 10 min for the switch to finish
	settleSamples           = 3  // ≈ 20 s of a clean host before tolerating status 4
)

var (
	switchStartRE    = regexp.MustCompile(`^switching to system configuration (/nix/store/\S+)$`)
	switchFinishedRE = regexp.MustCompile(`^finished switching to system configuration (/nix/store/\S+)$`)
	switchFailedRE   = regexp.MustCompile(`^switching to system configuration (/nix/store/\S+) failed \(status (\d+)\)$`)
)

// switchState is what the host's journal says about this deploy's switch.
type switchState struct {
	Target  string // the toplevel the switch started activating; "" = it never started
	Done    bool   // an outcome line was logged for Target
	Status  int    // switch-to-configuration's exit status when Done (0 = finished)
	Running bool   // the transient switch unit is still active
}

// hostEpoch reads the host's clock (seconds since the epoch) so the journal
// query only sees this deploy's switch. "" if it cannot be read.
func hostEpoch(h HostRunner, host string) string {
	out, err := h.Run(host, "date +%s")
	if err != nil {
		return ""
	}
	out = strings.TrimSpace(out)
	if _, err := strconv.ParseInt(out, 10, 64); err != nil {
		return ""
	}
	return out
}

// switchJournalCmd prints this deploy's switch milestones and the transient
// unit's activity. Without a host epoch it falls back to a 15-minute window,
// and parseSwitchJournal keeps only the LAST switch in it.
func switchJournalCmd(since string) string {
	window := "-15min"
	if since != "" {
		window = "@" + since
	}
	return "sudo journalctl -u " + switchUnit + " --since '" + window + "' --no-pager -o cat 2>/dev/null" +
		" | grep 'switching to system configuration'; " +
		"echo \"active=$(systemctl is-active " + switchUnit + " 2>/dev/null)\""
}

func parseSwitchJournal(out string) switchState {
	var st switchState
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if m := switchStartRE.FindStringSubmatch(line); m != nil {
			// A later switch supersedes an earlier one in the window.
			st = switchState{Target: m[1], Running: st.Running}
			continue
		}
		if m := switchFinishedRE.FindStringSubmatch(line); m != nil && m[1] == st.Target {
			st.Done, st.Status = true, 0
			continue
		}
		if m := switchFailedRE.FindStringSubmatch(line); m != nil && m[1] == st.Target {
			st.Done = true
			st.Status, _ = strconv.Atoi(m[2])
			continue
		}
		if state, ok := strings.CutPrefix(line, "active="); ok {
			switch strings.TrimSpace(state) {
			case "active", "activating", "reloading", "deactivating":
				st.Running = true
			}
		}
	}
	return st
}

// awaitSwitch reconnects to the host (bounded) and waits for this deploy's
// switch to log its outcome. ok=false means the host never came back.
func awaitSwitch(h HostRunner, host, since string, sleep func(time.Duration)) (st switchState, ok bool) {
	for i := 1; ; i++ {
		if _, err := h.Run(host, "true"); err == nil {
			break
		}
		if i >= verifyReconnectAttempts {
			return switchState{}, false
		}
		sleep(verifyDelay)
	}
	endedWithoutOutcome := 0
	for i := 1; i <= verifySwitchAttempts; i++ {
		out, err := h.Run(host, switchJournalCmd(since))
		if err == nil {
			st = parseSwitchJournal(out)
			switch {
			case st.Done:
				return st, true
			case st.Target == "" && !st.Running:
				// Nothing was ever activated: nixos-rebuild failed before the
				// switch (evaluation, build or copy).
				return st, true
			case !st.Running:
				// Ended without an outcome line (killed?). Look once more in
				// case the line is still on its way into the journal.
				if endedWithoutOutcome++; endedWithoutOutcome >= 2 {
					return st, true
				}
			default:
				endedWithoutOutcome = 0
			}
		}
		if i < verifySwitchAttempts {
			sleep(verifyDelay)
		}
	}
	return st, true
}

// currentSystem is the host's active toplevel ("" if unreadable).
func currentSystem(h HostRunner, host string) string {
	out, err := h.Run(host, "readlink -f /run/current-system")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// settleCmd samples the host's overall state in one round trip.
const settleCmd = `echo "state=$(systemctl is-system-running 2>/dev/null)"; ` +
	`echo "failed=$(systemctl list-units --type=service --state=failed --plain --no-legend 2>/dev/null | awk '{print $1}' | xargs)"; ` +
	`echo "restarting=$(systemctl list-units --all --type=service --state=auto-restart --plain --no-legend 2>/dev/null | awk '{print $1}' | xargs)"`

// settledHealthy reports whether the host is cleanly up over several samples:
// systemd says "running" (not degraded, not still starting), no service has
// failed, and none is waiting in auto-restart. One clean instant is not proof —
// a crash-looping service sits in auto-restart between attempts and never shows
// as failed until its start limit trips — so every sample must be clean.
func settledHealthy(h HostRunner, host string, sleep func(time.Duration)) (bool, string) {
	for i := 1; i <= settleSamples; i++ {
		out, err := h.Run(host, settleCmd)
		if err != nil {
			return false, "could not sample the host: " + err.Error()
		}
		fields := map[string]string{}
		for _, line := range strings.Split(out, "\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
				fields[k] = strings.TrimSpace(v)
			}
		}
		switch {
		case fields["state"] != "running":
			return false, "system state is " + strconv.Quote(fields["state"])
		case fields["failed"] != "":
			return false, "failed units: " + fields["failed"]
		case fields["restarting"] != "":
			return false, "units in auto-restart: " + fields["restarting"]
		}
		if i < settleSamples {
			sleep(verifyDelay)
		}
	}
	return true, ""
}

// unitsFailedDuringSwitch lists the services the journal saw fail since the
// deploy started (podman healthcheck transients and the switch unit itself left
// out), for the operator.
func unitsFailedDuringSwitch(h HostRunner, host, since string) string {
	window := "-15min"
	if since != "" {
		window = "@" + since
	}
	out, err := h.Run(host, "sudo journalctl --since '"+window+"' --no-pager -o cat 2>/dev/null"+
		" | grep -oE '^[A-Za-z0-9@._:-]+\\.service: Failed with result'"+
		" | sed 's/: Failed with result$//'"+
		" | grep -vE '^([0-9a-f]{64}-|"+strings.ReplaceAll(switchUnit, ".", "\\.")+"$)' | sort -u | xargs")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
