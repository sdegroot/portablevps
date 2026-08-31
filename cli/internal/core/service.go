package core

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ServiceEnv drives service data-movement (migrate/restore) over admin SSH. The
// HostRunner is host-parameterised, so one adapter reaches both source and
// target hosts.
type ServiceEnv struct {
	RepoRoot string
	Host     HostRunner
	Stream   StreamRunner
	Report   func(phase, status, msg string)
	// Certs checks/primes TLS certificates on a migration target before the
	// source is stopped, aborting the cutover early if none ever becomes
	// valid. Nil skips this safety check.
	Certs CertPrewarmer
	// SyncDNS repoints internal mesh DNS to host after a successful
	// migration. Nil leaves DNS repointing as a manual follow-up
	// (`network dns-sync`).
	SyncDNS func(host string) error
	// Sleep is called between certificate-validity poll attempts. Nil uses
	// time.Sleep; tests inject a no-op to avoid real delay.
	Sleep func(time.Duration)
}

func (env ServiceEnv) sleep() func(time.Duration) {
	if env.Sleep != nil {
		return env.Sleep
	}
	return time.Sleep
}

// CertPrewarmer probes a host for a valid TLS certificate on domain, used by
// Migrate to abort a cutover before the source is stopped if the target's
// certificate never becomes ready (a target with no valid cert would go live
// mid-outage-window with no earlier point to abort from).
type CertPrewarmer interface {
	// Prime makes an initial, unverified request to trigger certificate
	// issuance/loading for domain on host.
	Prime(domain, host string) error
	// Valid reports whether host now serves a trusted certificate for domain.
	Valid(domain, host string) (bool, error)
}

func (env ServiceEnv) report() func(string, string, string) {
	if env.Report == nil {
		return func(string, string, string) {}
	}
	return env.Report
}

// switchTo runs an in-place nixos-rebuild switch to profile on host, built on
// the remote.
func (env ServiceEnv) switchTo(profile, host string) error {
	nixSSHOpts := env.Host.NixSSHOpts()
	if routed, ok := env.Host.(interface{ NixSSHOptsFor(string) string }); ok {
		nixSSHOpts = routed.NixSSHOptsFor(host)
	}
	if err := env.Stream.Stream(env.RepoRoot,
		map[string]string{"NIX_SSHOPTS": nixSSHOpts},
		"nix", "--extra-experimental-features", "nix-command flakes",
		"run", NixpkgsFlake+"#nixos-rebuild", "--", "switch",
		"--flake", env.RepoRoot+"#"+profile,
		"--target-host", "admin@"+host,
		"--build-host", "admin@"+host,
		"--elevate=sudo", "--no-reexec"); err != nil {
		return err
	}
	if switched, ok := env.Host.(interface{ SwitchedTo(profile, host string) }); ok {
		switched.SwitchedTo(profile, host)
	}
	return nil
}

// MigrateOpts parameterises a service move between two running hosts.
type MigrateOpts struct {
	Server     string // target server config (the service's config)
	SourceHost string
	TargetHost string
	Marker     string // optional demo marker to verify before/after
}

// Migrate moves a service from SourceHost to TargetHost via its backup
// repository. It quiesces only source application writers while PostgreSQL
// remains live for the final physical backup, then stops the complete source
// stack, restores and starts the target. DNS repointing remains the final
// operator action, after this function has verified the target.
func Migrate(env ServiceEnv, o MigrateOpts) error {
	report := env.report()
	if o.Server == "" || o.SourceHost == "" || o.TargetHost == "" {
		return provisionErr(64, "target server, source host, and target host are required")
	}
	if err := verifyDistinctDrillMachines(env, o.SourceHost, o.TargetHost); err != nil {
		return provisionErr(64, "source and target identity check: %v", err)
	}

	sourceQuiesced := false
	targetStarted := false
	rollback := func(cause error) error {
		report("rollback", "run", "returning service ownership to "+o.SourceHost)
		if targetStarted {
			if _, err := env.Host.Run(o.TargetHost, "sudo systemctl stop apps.target"); err != nil {
				return provisionErr(70, "%v; rollback also failed stopping target apps: %v", cause, err)
			}
		}
		if sourceQuiesced {
			if _, err := env.Host.Run(o.SourceHost, "sudo systemctl start apps.target portablevps-backup.timer"); err != nil {
				return provisionErr(70, "%v; rollback also failed restarting source apps: %v", cause, err)
			}
		}
		report("rollback", "ok", "source stack restarted")
		return cause
	}

	report("prepare-target", "run", "switching "+o.TargetHost+" to restore mode")
	if err := env.switchTo(o.Server+"-restore", o.TargetHost); err != nil {
		return provisionErr(70, "switching target to restore mode: %v", err)
	}
	if _, err := env.Host.Run(o.TargetHost, `test "$(cat /etc/portablevps/restore-mode)" = true`); err != nil {
		return provisionErr(71, "target %s is not in restore mode: %v", o.TargetHost, err)
	}

	if env.Certs != nil {
		if err := prewarmProxyCertificates(env, o.TargetHost); err != nil {
			return err
		}
	}

	if o.Marker != "" {
		if _, err := env.Host.Run(o.SourceHost, "sudo verify-test-data.sh "+shellQuote(o.Marker)); err != nil {
			return provisionErr(71, "source marker verification failed: %v", err)
		}
	}

	// A scheduled backup after this point would make the target's selected final
	// snapshot ambiguous. Any currently-running backup remains serialized by its
	// lock and the explicit final backup below waits for it.
	if _, err := env.Host.Run(o.SourceHost, "sudo systemctl stop portablevps-backup.timer"); err != nil {
		return provisionErr(70, "stopping source backup timer: %v", err)
	}

	report("quiesce", "run", "stopping source writers while PostgreSQL remains live")
	// A failed quiesce may already have stopped some writers, so every outcome
	// from this point must restart the source stack on rollback.
	sourceQuiesced = true
	if _, err := env.Host.Run(o.SourceHost, "sudo portablevps-quiesce-writers"); err != nil {
		return rollback(provisionErr(70, "quiescing source writers: %v", err))
	}

	report("backup", "run", "final source backup on "+o.SourceHost)
	if _, err := env.Host.Run(o.SourceHost, "sudo systemctl start portablevps-backup.service"); err != nil {
		return rollback(provisionErr(70, "final source backup failed: %v", err))
	}

	report("cutover", "run", "stopping complete source stack after final backup")
	if _, err := env.Host.Run(o.SourceHost, "sudo systemctl stop apps.target"); err != nil {
		return rollback(provisionErr(70, "stopping source stack: %v", err))
	}

	report("restore", "run", "restoring the backup onto "+o.TargetHost)
	if _, err := env.Host.Run(o.TargetHost, "sudo restore.sh"); err != nil {
		return rollback(provisionErr(70, "restore on target failed: %v", err))
	}

	report("finalize", "run", "switching "+o.TargetHost+" to normal and starting apps")
	if err := env.switchTo(o.Server, o.TargetHost); err != nil {
		return rollback(provisionErr(70, "switching target to normal: %v", err))
	}
	if _, err := env.Host.Run(o.TargetHost, "sudo systemctl start apps.target"); err != nil {
		return rollback(provisionErr(70, "starting apps on target: %v", err))
	}
	targetStarted = true
	if o.Marker != "" {
		if _, err := env.Host.Run(o.TargetHost, "sudo verify-test-data.sh "+shellQuote(o.Marker)); err != nil {
			return rollback(provisionErr(71, "target marker verification failed: %v", err))
		}
	}
	if env.SyncDNS != nil {
		report("dns-sync", "run", "repointing internal DNS to "+o.TargetHost)
		if err := env.SyncDNS(o.TargetHost); err != nil {
			return provisionErr(70, "service migrated to %s but automatic DNS repoint failed: %v — run `network dns-sync` manually", o.TargetHost, err)
		}
		report("dns-sync", "ok", "internal DNS repointed to "+o.TargetHost)
	}
	reportMigrationDrill(env, o.TargetHost, report)

	dnsNote := "run `network dns-sync` to repoint DNS"
	if env.SyncDNS != nil {
		dnsNote = "DNS repointed automatically"
	}
	report("done", "ok", "migrated to "+o.TargetHost+" — "+dnsNote)
	return nil
}

// prewarmProxyCertificates triggers certificate issuance/loading for every
// domain the target's proxy plan declares and waits (up to 5 minutes per
// domain) for a trusted certificate to be served, before any source-side
// mutation happens. A domain that never gets a valid certificate aborts the
// migration early rather than cutting the service over into an outage.
func prewarmProxyCertificates(env ServiceEnv, targetHost string) error {
	report := env.report()
	planJSON, err := env.Host.Run(targetHost, "portablevps-proxy-domain-plan")
	if err != nil {
		return provisionErr(70, "reading proxy domain plan from %s: %v", targetHost, err)
	}
	domains, err := proxyPlanDomains(planJSON)
	if err != nil {
		return provisionErr(70, "%v", err)
	}
	if len(domains) == 0 {
		report("prewarm-tls", "info", "no proxy domains declared; skipping")
		return nil
	}
	sleep := env.sleep()
	for _, domain := range domains {
		report("prewarm-tls", "run", "triggering certificate load/issuance for "+domain)
		if err := env.Certs.Prime(domain, targetHost); err != nil {
			return provisionErr(70, "priming certificate for %s on %s: %v", domain, targetHost, err)
		}
		ready := false
		for attempt := 1; attempt <= 30; attempt++ {
			ok, err := env.Certs.Valid(domain, targetHost)
			if err != nil {
				return provisionErr(70, "checking certificate for %s on %s: %v", domain, targetHost, err)
			}
			if ok {
				ready = true
				break
			}
			if attempt < 30 {
				sleep(10 * time.Second)
			}
		}
		if !ready {
			return provisionErr(70, "%s did not present a valid TLS certificate for %s; aborting before stopping the source host", targetHost, domain)
		}
		report("prewarm-tls", "ok", "valid certificate for "+domain)
	}
	return nil
}

// proxyPlanDomains extracts the unique, non-empty domain names declared in a
// `portablevps-proxy-domain-plan` JSON document.
func proxyPlanDomains(planJSON string) ([]string, error) {
	var plan struct {
		Domains []struct {
			Domain string `json:"domain"`
		} `json:"domains"`
	}
	if err := json.Unmarshal([]byte(planJSON), &plan); err != nil {
		return nil, fmt.Errorf("parsing proxy domain plan: %w", err)
	}
	seen := map[string]bool{}
	var domains []string
	for _, entry := range plan.Domains {
		d := strings.TrimSpace(entry.Domain)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		domains = append(domains, d)
	}
	return domains, nil
}

// reportMigrationDrill best-effort-publishes the restore-drill success metric
// on targetHost after a verified migration, mirroring RestoreDrill's own
// report step. Failures here (including SSH errors) never fail the
// migration itself — the data movement already succeeded.
func reportMigrationDrill(env ServiceEnv, targetHost string, report func(phase, status, msg string)) {
	metricOut, err := env.Host.Run(targetHost, `if sudo systemctl cat portablevps-backup-restore-drill-report.service >/dev/null 2>&1; then sudo systemctl start portablevps-backup-restore-drill-report.service; echo reported; else echo unavailable; fi`)
	if err != nil {
		report("metric", "info", "could not report restore-drill metric: "+err.Error())
		return
	}
	if strings.TrimSpace(metricOut) == "reported" {
		report("metric", "ok", "published restore-drill success metric")
	} else {
		report("metric", "info", "restore-drill metric unit is not configured on "+targetHost)
	}
}

// RestoreOpts parameterises restoring a service onto an already-installed host.
type RestoreOpts struct {
	Server string
	Host   string
	Marker string
}

// Restore brings a service up on an already-installed host from its backup
// repository: put the host in restore mode, run restore.sh, switch to normal,
// start apps, and verify. Used for disaster recovery onto a fresh/replacement box.
func Restore(env ServiceEnv, o RestoreOpts) error {
	report := env.report()

	report("prepare", "run", "switching "+o.Host+" to restore mode")
	if err := env.switchTo(o.Server+"-restore", o.Host); err != nil {
		return provisionErr(70, "switching to restore mode: %v", err)
	}
	if _, err := env.Host.Run(o.Host, `test "$(cat /etc/portablevps/restore-mode)" = true`); err != nil {
		return provisionErr(71, "%s is not in restore mode: %v", o.Host, err)
	}

	report("restore", "run", "restoring the backup onto "+o.Host)
	if _, err := env.Host.Run(o.Host, "sudo restore.sh"); err != nil {
		return provisionErr(70, "restore failed: %v", err)
	}

	report("finalize", "run", "switching "+o.Host+" to normal and starting apps")
	if err := env.switchTo(o.Server, o.Host); err != nil {
		return provisionErr(70, "switching to normal: %v", err)
	}
	if _, err := env.Host.Run(o.Host, "sudo systemctl start apps.target"); err != nil {
		return provisionErr(70, "starting apps: %v", err)
	}
	if o.Marker != "" {
		if _, err := env.Host.Run(o.Host, "sudo verify-test-data.sh "+shellQuote(o.Marker)); err != nil {
			return provisionErr(71, "marker verification failed: %v", err)
		}
	}
	report("done", "ok", o.Host+" restored .#"+o.Server)
	return nil
}

// DrillOpts parameterises a real-infrastructure disaster-recovery drill.
type DrillOpts struct {
	Server      string
	SourceHost  string // live host to seed a marker on and back up (left otherwise untouched)
	RestoreHost string // host to restore onto and verify (destructive to its data)
	Marker      string // optional deterministic marker (tests); generated when empty
}

func newDrillMarker() (string, error) {
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("dr-%s-%x", time.Now().UTC().Format("20060102T150405Z"), suffix), nil
}

func backupComponentsPresent(env ServiceEnv, host string) (bool, error) {
	out, err := env.Host.Run(host, `if find /etc/portablevps/backups/paths.d -mindepth 1 -maxdepth 1 ! -type d -print -quit 2>/dev/null | grep -q .; then echo yes; else echo no; fi`)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "yes", nil
}

func postgresBackupPresent(env ServiceEnv, host string) (bool, error) {
	out, err := env.Host.Run(host, `if test -e /etc/portablevps/backups/paths.d/postgres; then echo yes; else echo no; fi`)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "yes", nil
}

func verifyDistinctDrillMachines(env ServiceEnv, sourceHost, restoreHost string) error {
	sourceID, err := env.Host.Run(sourceHost, "cat /etc/machine-id")
	if err != nil {
		return fmt.Errorf("reading source machine-id: %w", err)
	}
	restoreID, err := env.Host.Run(restoreHost, "cat /etc/machine-id")
	if err != nil {
		return fmt.Errorf("reading restore machine-id: %w", err)
	}
	sourceID = strings.TrimSpace(sourceID)
	restoreID = strings.TrimSpace(restoreID)
	if sourceID == "" || restoreID == "" {
		return fmt.Errorf("source or restore machine-id is empty")
	}
	if sourceID == restoreID {
		return provisionErr(64, "source and restore targets resolve to the same machine")
	}
	return nil
}

// seedBackupPathMarkers writes markers inside registered directories and
// returns sha256sum evidence for registered files. Files such as Traefik's
// acme.json cannot safely be modified just to plant a marker, so their exact
// pre-backup content becomes the evidence verified after restore.
func seedBackupPathMarkers(env ServiceEnv, host, marker string) (string, error) {
	command := `set -eu
marker=` + shellQuote(marker) + `
for manifest in /etc/portablevps/backups/paths.d/*; do
  [ -e "$manifest" ] || continue
  [ "$(basename "$manifest")" != postgres ] || continue
  while IFS= read -r path; do
    [ -n "$path" ] || continue
    if [ -d "$path" ]; then
      printf '%s\n' "$marker" | sudo tee "$path/dr-marker.txt" >/dev/null
    elif [ -f "$path" ]; then
      sudo sha256sum "$path"
    else
      echo "registered backup path does not exist: $path" >&2
      exit 1
    fi
  done < "$manifest"
done`
	out, err := env.Host.Run(host, command)
	return strings.TrimSpace(out), err
}

func verifyBackupPathMarkers(env ServiceEnv, host, marker, expectedFileEvidence string) error {
	command := `set -eu
marker=` + shellQuote(marker) + `
rc=0
for manifest in /etc/portablevps/backups/paths.d/*; do
  [ -e "$manifest" ] || continue
  [ "$(basename "$manifest")" != postgres ] || continue
  while IFS= read -r path; do
    [ -n "$path" ] || continue
    if [ -d "$path" ]; then
      got="$(sudo cat "$path/dr-marker.txt" 2>/dev/null || true)"
      if [ "$got" != "$marker" ]; then
        echo "DR marker missing from $path" >&2
        rc=1
      fi
    elif [ -f "$path" ]; then
      sudo sha256sum "$path"
    else
      echo "registered backup path was not restored: $path" >&2
      rc=1
    fi
  done < "$manifest"
done
exit "$rc"`
	out, err := env.Host.Run(host, command)
	if err != nil {
		return err
	}
	actualFileEvidence := strings.TrimSpace(out)
	if actualFileEvidence != expectedFileEvidence {
		return fmt.Errorf("registered file content differs after restore")
	}
	return nil
}

func runDrillHooks(env ServiceEnv, host, phase, marker string) error {
	command := `set -eu
hook_dir=/etc/portablevps/dr/` + phase + `.d
[ -d "$hook_dir" ] || exit 0
find "$hook_dir" -mindepth 1 -maxdepth 1 ! -type d -print | sort |
while IFS= read -r hook; do
  sudo "$hook" ` + shellQuote(marker) + `
done`
	_, err := env.Host.Run(host, command)
	return err
}

// RestoreDrill proves backups actually restore, end to end, against real hosts:
// it seeds a unique marker into PostgreSQL (when registered), every declared
// file backup path, and app-owned seed hooks; runs the production backup
// service; then restores onto RestoreHost and verifies every marker and
// app-owned hook. The source is only seeded (non-destructive); the restore host
// is rebuilt from the backup. It returns the marker it used.
func RestoreDrill(env ServiceEnv, o DrillOpts) (string, error) {
	report := env.report()
	if o.SourceHost == "" || o.RestoreHost == "" {
		return "", provisionErr(64, "source and restore hosts are required")
	}
	if o.SourceHost == o.RestoreHost {
		return "", provisionErr(64, "source and restore hosts must be different")
	}
	if err := verifyDistinctDrillMachines(env, o.SourceHost, o.RestoreHost); err != nil {
		if _, ok := err.(*ProvisionError); ok {
			return "", err
		}
		return "", provisionErr(70, "verifying distinct source and restore machines: %v", err)
	}

	components, err := backupComponentsPresent(env, o.SourceHost)
	if err != nil {
		return "", provisionErr(70, "checking backup components on %s: %v", o.SourceHost, err)
	}
	if !components {
		return "", provisionErr(69, "no backup components are registered on %s", o.SourceHost)
	}
	hasPostgres, err := postgresBackupPresent(env, o.SourceHost)
	if err != nil {
		return "", provisionErr(70, "checking PostgreSQL backup registration on %s: %v", o.SourceHost, err)
	}

	marker := o.Marker
	if marker == "" {
		marker, err = newDrillMarker()
		if err != nil {
			return "", provisionErr(70, "generating restore-drill marker: %v", err)
		}
	}
	report("seed", "run", "seeding declared state on "+o.SourceHost)
	if hasPostgres {
		if _, err := env.Host.Run(o.SourceHost, "sudo insert-test-data.sh "+shellQuote(marker)+" >/dev/null"); err != nil {
			return marker, provisionErr(70, "seeding PostgreSQL marker on %s: %v", o.SourceHost, err)
		}
	}
	fileEvidence, err := seedBackupPathMarkers(env, o.SourceHost, marker)
	if err != nil {
		return marker, provisionErr(70, "seeding registered backup paths on %s: %v", o.SourceHost, err)
	}
	if err := runDrillHooks(env, o.SourceHost, "seed", marker); err != nil {
		return marker, provisionErr(70, "running app DR seed hooks on %s: %v", o.SourceHost, err)
	}
	report("seed", "ok", "marker "+marker)

	report("backup", "run", "starting production backup service on "+o.SourceHost)
	if _, err := env.Host.Run(o.SourceHost, "sudo systemctl start portablevps-backup.service"); err != nil {
		return marker, provisionErr(70, "backup on %s: %v", o.SourceHost, err)
	}

	// Restore onto the (separate) restore host and verify the marker survived.
	restoreMarker := ""
	if hasPostgres {
		restoreMarker = marker
	}
	if err := Restore(env, RestoreOpts{Server: o.Server, Host: o.RestoreHost, Marker: restoreMarker}); err != nil {
		return marker, err
	}
	report("verify", "run", "verifying all declared state on "+o.RestoreHost)
	if err := verifyBackupPathMarkers(env, o.RestoreHost, marker, fileEvidence); err != nil {
		return marker, provisionErr(71, "registered backup-path verification failed on %s: %v", o.RestoreHost, err)
	}
	if err := runDrillHooks(env, o.RestoreHost, "verify", marker); err != nil {
		return marker, provisionErr(71, "app DR verification hooks failed on %s: %v", o.RestoreHost, err)
	}
	report("verify", "ok", "all declared state contains marker "+marker)

	metricOut, err := env.Host.Run(o.RestoreHost, `if sudo systemctl cat portablevps-backup-restore-drill-report.service >/dev/null 2>&1; then sudo systemctl start portablevps-backup-restore-drill-report.service; echo reported; else echo unavailable; fi`)
	if err != nil {
		return marker, provisionErr(70, "reporting successful restore drill from %s: %v", o.RestoreHost, err)
	}
	if strings.TrimSpace(metricOut) == "reported" {
		report("metric", "ok", "published restore-drill success metric")
	} else {
		report("metric", "info", "restore-drill metric unit is not configured on "+o.RestoreHost)
	}
	return marker, nil
}
