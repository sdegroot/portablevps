package core

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestMigrateOrdering(t *testing.T) {
	host := &fakeHost{}
	stream := &recordRunner{}
	err := Migrate(ServiceEnv{RepoRoot: "/repo", Host: host, Stream: stream},
		MigrateOpts{Server: "svc", SourceHost: "src", TargetHost: "dst", Marker: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	seq := strings.Join(host.runs, " | ")
	// Key ordering invariants: restore-mode check, writer quiesce while
	// PostgreSQL is live, source backup, then full source stop and restore.
	for _, want := range []string{
		"restore-mode", "portablevps-quiesce-writers", "portablevps-backup.service", "stop apps.target", "restore.sh", "start apps.target",
	} {
		if !strings.Contains(seq, want) {
			t.Errorf("migrate did not run %q; sequence: %s", want, seq)
		}
	}
	// two switches: restore profile then normal
	if !stream.sawContaining("#svc-restore") || !stream.sawContaining("#svc") {
		t.Errorf("expected switches to svc-restore then svc: %v", stream.calls)
	}
	if strings.Index(seq, "portablevps-quiesce-writers") > strings.Index(seq, "portablevps-backup.service") {
		t.Errorf("writers must be quiesced before the final backup: %s", seq)
	}
	if strings.Index(seq, "portablevps-backup.service") > strings.Index(seq, "stop apps.target") {
		t.Errorf("full source stack must stop only after the final backup: %s", seq)
	}
}

func TestMigrateRestartsSourceWhenFinalBackupFails(t *testing.T) {
	host := &fakeHost{failContaining: "portablevps-backup.service"}
	err := Migrate(ServiceEnv{RepoRoot: "/repo", Host: host, Stream: &recordRunner{}},
		MigrateOpts{Server: "svc", SourceHost: "src", TargetHost: "dst"})
	if err == nil {
		t.Fatal("expected final backup failure")
	}
	sequence := strings.Join(host.runs, " | ")
	if !strings.Contains(sequence, "portablevps-quiesce-writers") || !strings.Contains(sequence, "start apps.target portablevps-backup.timer") {
		t.Fatalf("failed migration must restore the quiesced source: %s", sequence)
	}
	if strings.Contains(sequence, "restore.sh") {
		t.Fatalf("migration must not restore target after final backup failure: %s", sequence)
	}
}

func TestMigrateRejectsSameMachineBeforeSwitchingTarget(t *testing.T) {
	host := &fakeHost{machineIDs: map[string]string{"src": "same", "dst": "same"}}
	stream := &recordRunner{}
	err := Migrate(ServiceEnv{RepoRoot: "/repo", Host: host, Stream: stream},
		MigrateOpts{Server: "svc", SourceHost: "src", TargetHost: "dst"})
	if err == nil {
		t.Fatal("expected same-machine rejection")
	}
	if len(stream.calls) != 0 {
		t.Fatalf("same-machine check must happen before target switch: %v", stream.calls)
	}
}

func TestMigrateAbortsIfTargetNotInRestoreMode(t *testing.T) {
	host := &fakeHost{failRestoreModeCheck: true}
	stream := &recordRunner{}
	err := Migrate(ServiceEnv{RepoRoot: "/repo", Host: host, Stream: stream},
		MigrateOpts{Server: "svc", SourceHost: "src", TargetHost: "dst"})
	if err == nil {
		t.Fatal("expected abort when target is not in restore mode")
	}
	if strings.Contains(strings.Join(host.runs, " "), "restore.sh") {
		t.Error("must not restore when the target is not confirmed in restore mode")
	}
}

// fakeCertPrewarmer simulates certificate priming/validity for Migrate's
// pre-cutover TLS check without touching the network.
type fakeCertPrewarmer struct {
	primed        []string // domains Prime was called for, in order
	validAttempts int      // calls to Valid so far
	validAfter    int      // Valid returns true starting at this call count (0 = always true, <0 = never)
	primeErr      error
	validErr      error
}

func (f *fakeCertPrewarmer) Prime(domain, _host string) error {
	f.primed = append(f.primed, domain)
	return f.primeErr
}

func (f *fakeCertPrewarmer) Valid(_domain, _host string) (bool, error) {
	f.validAttempts++
	if f.validErr != nil {
		return false, f.validErr
	}
	if f.validAfter < 0 {
		return false, nil
	}
	return f.validAttempts >= f.validAfter, nil
}

const planWithOneDomain = `{"domains":[{"domain":"svc.example.com"}]}`

func TestMigratePrewarmsCertificatesBeforeQuiescingSource(t *testing.T) {
	certs := &fakeCertPrewarmer{validAfter: 1}
	host := &fakeHost{outputs: map[string]string{"portablevps-proxy-domain-plan": planWithOneDomain}}
	err := Migrate(ServiceEnv{RepoRoot: "/repo", Host: host, Stream: &recordRunner{}, Certs: certs},
		MigrateOpts{Server: "svc", SourceHost: "src", TargetHost: "dst"})
	if err != nil {
		t.Fatal(err)
	}
	if len(certs.primed) != 1 || certs.primed[0] != "svc.example.com" {
		t.Fatalf("expected certificate priming for the plan's domain, got %v", certs.primed)
	}
	sequence := strings.Join(host.runs, " | ")
	if strings.Index(sequence, "portablevps-proxy-domain-plan") > strings.Index(sequence, "portablevps-quiesce-writers") {
		t.Errorf("certificate check must run before source writers are quiesced: %s", sequence)
	}
}

func TestMigrateAbortsBeforeTouchingSourceWhenCertificateNeverBecomesValid(t *testing.T) {
	certs := &fakeCertPrewarmer{validAfter: -1}
	host := &fakeHost{outputs: map[string]string{"portablevps-proxy-domain-plan": planWithOneDomain}}
	err := Migrate(ServiceEnv{RepoRoot: "/repo", Host: host, Stream: &recordRunner{}, Certs: certs, Sleep: func(time.Duration) {}},
		MigrateOpts{Server: "svc", SourceHost: "src", TargetHost: "dst"})
	if err == nil {
		t.Fatal("expected abort when no valid certificate ever appears")
	}
	if certs.validAttempts != 30 {
		t.Errorf("expected all 30 poll attempts, got %d", certs.validAttempts)
	}
	sequence := strings.Join(host.runs, " | ")
	if strings.Contains(sequence, "portablevps-quiesce-writers") || strings.Contains(sequence, "portablevps-backup.service") {
		t.Errorf("a failed certificate check must abort before any source-side mutation: %s", sequence)
	}
}

func TestMigrateSkipsCertificateCheckWhenNoCertsInjected(t *testing.T) {
	host := &fakeHost{outputs: map[string]string{"portablevps-proxy-domain-plan": planWithOneDomain}}
	err := Migrate(ServiceEnv{RepoRoot: "/repo", Host: host, Stream: &recordRunner{}},
		MigrateOpts{Server: "svc", SourceHost: "src", TargetHost: "dst"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(host.runs, " | "), "portablevps-proxy-domain-plan") {
		t.Error("must not fetch the proxy domain plan when Certs is nil")
	}
}

func TestMigrateSyncsDNSAfterVerificationAndReportsDrillMetric(t *testing.T) {
	host := &fakeHost{outputs: map[string]string{
		"portablevps-backup-restore-drill-report": "reported\n",
	}}
	var syncedHost string
	err := Migrate(ServiceEnv{RepoRoot: "/repo", Host: host, Stream: &recordRunner{}, SyncDNS: func(h string) error {
		syncedHost = h
		return nil
	}}, MigrateOpts{Server: "svc", SourceHost: "src", TargetHost: "dst", Marker: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	if syncedHost != "dst" {
		t.Fatalf("expected SyncDNS called with the target host, got %q", syncedHost)
	}
	sequence := strings.Join(host.runs, " | ")
	if !strings.Contains(sequence, "portablevps-backup-restore-drill-report.service") {
		t.Errorf("expected the restore-drill metric to be reported: %s", sequence)
	}
}

func TestMigrateSurfacesSyncDNSFailureWithoutRollingBackAVerifiedTarget(t *testing.T) {
	host := &fakeHost{}
	err := Migrate(ServiceEnv{RepoRoot: "/repo", Host: host, Stream: &recordRunner{}, SyncDNS: func(string) error {
		return fmt.Errorf("netbird: 401 unauthorized")
	}}, MigrateOpts{Server: "svc", SourceHost: "src", TargetHost: "dst"})
	if err == nil {
		t.Fatal("expected the DNS sync failure to surface")
	}
	if !strings.Contains(err.Error(), "netbird: 401 unauthorized") {
		t.Errorf("expected the underlying DNS sync error in the message, got %v", err)
	}
	sequence := strings.Join(host.runs, " | ")
	if strings.Contains(sequence, "stop apps.target") && strings.Count(sequence, "stop apps.target") > 1 {
		t.Errorf("a DNS-sync failure after target verification must not roll back the already-verified target: %s", sequence)
	}
	if strings.Contains(sequence, "start apps.target portablevps-backup.timer") {
		t.Errorf("a DNS-sync failure after target verification must not restart the (now stale) source: %s", sequence)
	}
}

func TestMigrateDrillMetricFailureIsBestEffort(t *testing.T) {
	host := &fakeHost{failContaining: "portablevps-backup-restore-drill-report"}
	err := Migrate(ServiceEnv{RepoRoot: "/repo", Host: host, Stream: &recordRunner{}},
		MigrateOpts{Server: "svc", SourceHost: "src", TargetHost: "dst"})
	if err != nil {
		t.Fatalf("a failed metric report must not fail the migration: %v", err)
	}
}

type routedHost struct {
	fakeHost
}

func (r *routedHost) NixSSHOptsFor(host string) string {
	return "-i key-for-" + host
}

func TestRestoreUsesHostSpecificNixSSHOptsWhenAvailable(t *testing.T) {
	host := &routedHost{}
	stream := &recordRunner{}
	err := Restore(ServiceEnv{RepoRoot: "/repo", Host: host, Stream: stream},
		RestoreOpts{Server: "svc", Host: "restore-host"})
	if err != nil {
		t.Fatal(err)
	}
	if !stream.sawContaining("NIX_SSHOPTS=-i key-for-restore-host") {
		t.Fatalf("expected restore host SSH options, got %v", stream.calls)
	}
}

type switchingHost struct {
	fakeHost
	switched bool
}

func (s *switchingHost) NixSSHOptsFor(host string) string {
	if s.switched {
		return "-i after-switch-for-" + host
	}
	return "-i before-switch-for-" + host
}

func (s *switchingHost) SwitchedTo(_profile, _host string) {
	s.switched = true
}

func TestRestoreCanChangeSSHOptsAfterFirstSwitch(t *testing.T) {
	host := &switchingHost{}
	stream := &recordRunner{}
	err := Restore(ServiceEnv{RepoRoot: "/repo", Host: host, Stream: stream},
		RestoreOpts{Server: "svc", Host: "restore-host"})
	if err != nil {
		t.Fatal(err)
	}
	if !stream.sawContaining("NIX_SSHOPTS=-i before-switch-for-restore-host") {
		t.Fatalf("expected initial restore-host SSH options, got %v", stream.calls)
	}
	if !stream.sawContaining("NIX_SSHOPTS=-i after-switch-for-restore-host") {
		t.Fatalf("expected post-switch service SSH options, got %v", stream.calls)
	}
}

func TestRestoreDrillUsesProductionServiceAndVerifiesAllDeclaredState(t *testing.T) {
	host := &fakeHost{outputs: map[string]string{
		"find /etc/portablevps/backups/paths.d":   "yes\n",
		"paths.d/postgres; then echo yes":         "yes\n",
		"portablevps-backup-restore-drill-report": "reported\n",
	}}
	stream := &recordRunner{}
	marker, err := RestoreDrill(
		ServiceEnv{RepoRoot: "/repo", Host: host, Stream: stream},
		DrillOpts{Server: "svc", SourceHost: "source", RestoreHost: "restore", Marker: "dr-test"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if marker != "dr-test" {
		t.Fatalf("unexpected marker %q", marker)
	}
	sequence := strings.Join(host.runs, " | ")
	for _, want := range []string{
		"insert-test-data.sh",
		"dr-marker.txt",
		"/etc/portablevps/dr/seed.d",
		"systemctl start portablevps-backup.service",
		"restore.sh",
		"verify-test-data.sh",
		"/etc/portablevps/dr/verify.d",
		"portablevps-backup-restore-drill-report.service",
	} {
		if !strings.Contains(sequence, want) {
			t.Errorf("restore drill did not run %q; sequence: %s", want, sequence)
		}
	}
	if strings.Contains(sequence, "sudo backup.sh") || strings.Contains(sequence, "init-backup-repo.sh") {
		t.Errorf("restore drill bypassed the production backup service: %s", sequence)
	}
	if strings.LastIndex(sequence, "portablevps-backup-restore-drill-report.service") <
		strings.LastIndex(sequence, "/etc/portablevps/dr/verify.d") {
		t.Errorf("success metric must follow all verification: %s", sequence)
	}
}

func TestRestoreDrillSupportsNonPostgresComponents(t *testing.T) {
	host := &fakeHost{outputs: map[string]string{
		"find /etc/portablevps/backups/paths.d": "yes\n",
		"paths.d/postgres; then echo yes":       "no\n",
	}}
	_, err := RestoreDrill(
		ServiceEnv{RepoRoot: "/repo", Host: host, Stream: &recordRunner{}},
		DrillOpts{Server: "svc", SourceHost: "source", RestoreHost: "restore", Marker: "dr-test"},
	)
	if err != nil {
		t.Fatal(err)
	}
	sequence := strings.Join(host.runs, " | ")
	if strings.Contains(sequence, "insert-test-data.sh") || strings.Contains(sequence, "verify-test-data.sh") {
		t.Errorf("non-PostgreSQL drill ran PostgreSQL marker helpers: %s", sequence)
	}
	if !strings.Contains(sequence, "dr-marker.txt") {
		t.Errorf("non-PostgreSQL state was not verified: %s", sequence)
	}
}

func TestRestoreDrillRejectsSameSourceAndRestoreHost(t *testing.T) {
	host := &fakeHost{}
	_, err := RestoreDrill(
		ServiceEnv{RepoRoot: "/repo", Host: host, Stream: &recordRunner{}},
		DrillOpts{Server: "svc", SourceHost: "same", RestoreHost: "same"},
	)
	if e, ok := err.(*ProvisionError); !ok || e.ExitCode() != 64 {
		t.Fatalf("expected exit 64, got %v", err)
	}
	if len(host.runs) != 0 {
		t.Fatalf("same-host drill must fail before remote mutation: %v", host.runs)
	}
}

func TestRestoreDrillRejectsAliasesForSameMachine(t *testing.T) {
	host := &fakeHost{machineIDs: map[string]string{
		"source-name":  "same-machine-id",
		"restore-name": "same-machine-id",
	}}
	_, err := RestoreDrill(
		ServiceEnv{RepoRoot: "/repo", Host: host, Stream: &recordRunner{}},
		DrillOpts{Server: "svc", SourceHost: "source-name", RestoreHost: "restore-name"},
	)
	if e, ok := err.(*ProvisionError); !ok || e.ExitCode() != 64 {
		t.Fatalf("expected exit 64, got %v", err)
	}
	if len(host.runs) != 2 {
		t.Fatalf("alias collision must fail after read-only identity probes: %v", host.runs)
	}
}

func TestRestoreDrillRequiresRegisteredBackupComponents(t *testing.T) {
	host := &fakeHost{outputs: map[string]string{
		"find /etc/portablevps/backups/paths.d": "no\n",
	}}
	_, err := RestoreDrill(
		ServiceEnv{RepoRoot: "/repo", Host: host, Stream: &recordRunner{}},
		DrillOpts{Server: "svc", SourceHost: "source", RestoreHost: "restore"},
	)
	if e, ok := err.(*ProvisionError); !ok || e.ExitCode() != 69 {
		t.Fatalf("expected exit 69, got %v", err)
	}
}

type changedFileEvidenceHost struct {
	fakeHost
	evidenceReads int
}

func (h *changedFileEvidenceHost) Run(host, command string) (string, error) {
	if strings.Contains(command, "sha256sum") {
		h.runs = append(h.runs, command)
		h.evidenceReads++
		if h.evidenceReads == 1 {
			return "source-hash  /var/lib/app/state.json\n", nil
		}
		return "changed-hash  /var/lib/app/state.json\n", nil
	}
	return h.fakeHost.Run(host, command)
}

func TestRestoreDrillRejectsChangedRegisteredFile(t *testing.T) {
	host := &changedFileEvidenceHost{fakeHost: fakeHost{outputs: map[string]string{
		"find /etc/portablevps/backups/paths.d": "yes\n",
		"paths.d/postgres; then echo yes":       "no\n",
	}}}
	_, err := RestoreDrill(
		ServiceEnv{RepoRoot: "/repo", Host: host, Stream: &recordRunner{}},
		DrillOpts{Server: "svc", SourceHost: "source", RestoreHost: "restore", Marker: "dr-test"},
	)
	if e, ok := err.(*ProvisionError); !ok || e.ExitCode() != 71 {
		t.Fatalf("expected verification exit 71, got %v", err)
	}
	sequence := strings.Join(host.runs, " | ")
	if strings.Contains(sequence, "portablevps-backup-restore-drill-report.service") {
		t.Fatalf("failed verification must not publish success metric: %s", sequence)
	}
}
