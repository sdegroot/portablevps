package core

import (
	"strings"
	"testing"
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
	// key ordering invariants: restore-mode check, stop source, source backup,
	// restore on target, start apps.
	for _, want := range []string{
		"restore-mode", "stop apps.target", "portablevps-backup.service", "restore.sh", "start apps.target",
	} {
		if !strings.Contains(seq, want) {
			t.Errorf("migrate did not run %q; sequence: %s", want, seq)
		}
	}
	// two switches: restore profile then normal
	if !stream.sawContaining("#svc-restore") || !stream.sawContaining("#svc") {
		t.Errorf("expected switches to svc-restore then svc: %v", stream.calls)
	}
	// the source is stopped BEFORE the final backup so the snapshot is consistent
	// (no writes can land between the backup and the cutover).
	if strings.Index(seq, "stop apps.target") > strings.Index(seq, "portablevps-backup.service") {
		t.Errorf("source must be stopped before the final backup: %s", seq)
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
