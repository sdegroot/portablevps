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
