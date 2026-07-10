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
	// key ordering invariants: restore-mode check, source backup, stop source,
	// restore on target, start apps.
	for _, want := range []string{
		"restore-mode", "portablevps-backup.service", "stop apps.target", "restore.sh", "start apps.target",
	} {
		if !strings.Contains(seq, want) {
			t.Errorf("migrate did not run %q; sequence: %s", want, seq)
		}
	}
	// two switches: restore profile then normal
	if !stream.sawContaining("#svc-restore") || !stream.sawContaining("#svc") {
		t.Errorf("expected switches to svc-restore then svc: %v", stream.calls)
	}
	// backup happens before the source stop
	if strings.Index(seq, "portablevps-backup.service") > strings.Index(seq, "stop apps.target") {
		t.Errorf("final backup must precede stopping the source: %s", seq)
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
