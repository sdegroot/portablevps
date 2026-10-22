package core

import "strings"

// failedUnitsProbe inspects the host's failed units and decides whether they are
// only the known podman healthcheck false-positive: when a container (re)starts,
// podman's transient healthcheck unit can fire while the container is still
// inside its HealthStartPeriod, and `podman healthcheck run` exits 1 (a podman
// bug fixed only in v6 — the start period suppresses the container KILL but not
// the process exit code). That leaves a failed <id>-<hash>.service behind which
// switch-to-configuration counts against a good switch, and a reboot's
// failed-unit check counts against a good boot.
//
// It emits diagnostic lines then a final `verdict=benign units=<names>` (when
// EVERY failed unit is a podman healthcheck transient whose container reaches
// healthy) or `verdict=real reason=...`. It waits out a still-"starting"
// container (bounded) rather than mistaking a slow boot for a real failure —
// only the true-positive path polls, so a genuine failure returns promptly.
const failedUnitsProbe = `sudo bash -c '
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

// failedUnitsVerdict is failedUnitsProbe's parsed answer.
type failedUnitsVerdict struct {
	// Benign: every failed unit is a podman healthcheck transient whose
	// container converged to healthy. Units lists them (space-separated, for
	// `systemctl reset-failed`).
	Benign bool
	Units  string
	// Reason is the probe's reason when not benign ("no-failed-units",
	// "non-healthcheck-unit", "container-not-healthy", ...). Empty when the host
	// could not be probed — can't verify => treat as real.
	Reason string
}

// probeFailedUnits runs failedUnitsProbe on host and parses its verdict.
func probeFailedUnits(h HostRunner, host string) failedUnitsVerdict {
	out, err := h.Run(host, failedUnitsProbe)
	if err != nil {
		return failedUnitsVerdict{}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "verdict=benign units="); ok {
			if u := strings.TrimSpace(rest); u != "" {
				return failedUnitsVerdict{Benign: true, Units: u}
			}
			return failedUnitsVerdict{}
		}
		if rest, ok := strings.CutPrefix(line, "verdict=real"); ok {
			reason := ""
			for _, field := range strings.Fields(rest) {
				if r, ok := strings.CutPrefix(field, "reason="); ok {
					reason = r
				}
			}
			return failedUnitsVerdict{Reason: reason}
		}
	}
	return failedUnitsVerdict{}
}

// benignHealthcheckFailure reports whether the host's failed units are only the
// tolerated podman healthcheck false-positive, returning the failed transient
// unit names plus a short human detail. Returns "" for units on a genuine
// failure or if the host cannot be probed.
func benignHealthcheckFailure(h HostRunner, host string) (units, detail string) {
	v := probeFailedUnits(h, host)
	if !v.Benign {
		return "", ""
	}
	return v.Units, "restarted container healthy; failed unit(s): " + v.Units
}

// clearHealthcheckTransients resets the stale failed transient units so they
// neither linger in `systemctl --failed` nor re-trigger alerting. (podman also
// clears them on the container's next healthcheck run; this just doesn't wait.)
func clearHealthcheckTransients(h HostRunner, host, units string) {
	_, _ = h.Run(host, "sudo systemctl reset-failed "+units+" || true")
}
