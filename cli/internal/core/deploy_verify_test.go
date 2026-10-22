package core

import (
	"strings"
	"testing"
)

// deployWith runs a deploy against host whose nixos-rebuild exits non-zero
// when fail is set, returning the reports by status.
func deployWith(host *fakeHost, fail bool) (map[string][]string, error) {
	report, got := reports()
	err := Deploy(DeployEnv{RepoRoot: "/repo", Runner: &recordRunner{fail: fail}, Host: host, Report: report, Sleep: noSleep}, "web", "1.2.3.4")
	return got, err
}

func exitCode(err error) int {
	if e, ok := err.(*DeployError); ok {
		return e.ExitCode()
	}
	return -1
}

func TestDeploySSHDropButSwitchFinishedPasses(t *testing.T) {
	// The switch restarted netbird, the mesh SSH session died (nixos-rebuild
	// exit 255), admin SSH came back after two attempts, and the host's journal
	// says the switch finished on the generation that is now current.
	host := &fakeHost{epoch: "1792670000", dropped: 2, switchJournal: switched(0), outputs: onTarget()}
	got, err := deployWith(host, true)
	if err != nil {
		t.Fatalf("a finished switch must pass despite the SSH drop, got %v", err)
	}
	joined := strings.Join(host.runs, " | ")
	if !strings.Contains(joined, "--since '@1792670000'") {
		t.Errorf("expected the journal query scoped to the deploy's start, got %q", joined)
	}
	if host.trueCalls != 4 { // preflight + 2 failed reconnects + 1 good
		t.Errorf("expected 3 reconnect attempts, got %d true calls", host.trueCalls)
	}
	if !strings.Contains(joined, "deploy-report-success") || strings.Contains(joined, "deploy-report-failure") {
		t.Errorf("expected only the success report, got %q", joined)
	}
	if len(got["warn"]) == 0 || !strings.Contains(got["warn"][0], "finished the switch") {
		t.Errorf("expected a warning that the outcome was read from the host, got %v", got["warn"])
	}
}

func TestDeploySwitchThatNeverStartedFails(t *testing.T) {
	// Evaluation/build/copy failed: nothing in the journal, no unit running.
	host := &fakeHost{epoch: "1792670000", switchJournal: "active=inactive", outputs: onTarget()}
	_, err := deployWith(host, true)
	if exitCode(err) != 70 || !strings.Contains(err.Error(), "before activating anything") {
		t.Fatalf("expected exit 70 for a switch that never started, got %v", err)
	}
	if !strings.Contains(strings.Join(host.runs, " | "), "deploy-report-failure") {
		t.Error("expected the failure report")
	}
}

func TestDeployWithoutHostEpochFallsBackToWindow(t *testing.T) {
	host := &fakeHost{switchJournal: switched(0), outputs: onTarget()}
	if _, err := deployWith(host, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(host.runs, " | "), "--since '-15min'") {
		t.Errorf("expected the 15-minute fallback window, got %v", host.runs)
	}
}

func TestDeployHostNeverReturnsFails(t *testing.T) {
	host := &fakeHost{dropped: 1000}
	_, err := deployWith(host, true)
	if exitCode(err) != 70 || !strings.Contains(err.Error(), "did not come back") {
		t.Fatalf("expected exit 70 when the host never comes back, got %v", err)
	}
	if host.trueCalls != 1+verifyReconnectAttempts {
		t.Errorf("reconnects must be bounded at %d, got %d", verifyReconnectAttempts, host.trueCalls-1)
	}
}

func TestDeployOtherGenerationCurrentFails(t *testing.T) {
	host := &fakeHost{switchJournal: switched(0),
		outputs: map[string]string{"readlink -f /run/current-system": "/nix/store/zzz-other"}}
	_, err := deployWith(host, true)
	if exitCode(err) != 70 || !strings.Contains(err.Error(), "not the generation just switched to") {
		t.Fatalf("expected a generation mismatch failure, got %v", err)
	}
}

func TestDeploySwitchStillRunningFails(t *testing.T) {
	host := &fakeHost{switchJournal: "switching to system configuration " + testTarget + "\nactive=active", outputs: onTarget()}
	_, err := deployWith(host, true)
	if exitCode(err) != 70 || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("expected a still-running failure, got %v", err)
	}
}

func TestDeploySwitchKilledWithoutOutcomeFails(t *testing.T) {
	host := &fakeHost{switchJournal: "switching to system configuration " + testTarget + "\nactive=inactive", outputs: onTarget()}
	_, err := deployWith(host, true)
	if exitCode(err) != 70 || !strings.Contains(err.Error(), "without an outcome") {
		t.Fatalf("expected a no-outcome failure, got %v", err)
	}
}

func TestDeployActivationErrorFails(t *testing.T) {
	// Status other than 4: the activation script itself failed.
	host := &fakeHost{switchJournal: switched(1), outputs: onTarget()}
	_, err := deployWith(host, true)
	if exitCode(err) != 70 || !strings.Contains(err.Error(), "status 1") {
		t.Fatalf("expected a status-1 failure, got %v", err)
	}
}

func TestDeployStatus4RecoveredUnitsPassesWithWarning(t *testing.T) {
	// The authentik case: units failed during the switch (a proxy exiting 143
	// on stop, containers hitting a podman race) and recovered through
	// Restart=; nothing is failed now and the host stays cleanly running.
	host := &fakeHost{
		switchJournal:      switched(4),
		outputs:            onTarget(),
		probeOut:           "verdict=real reason=no-failed-units",
		settleOut:          "state=running\nfailed=\nrestarting=",
		failedDuringSwitch: "authentik-worker.service postgres-netbird-5432-proxy.service",
	}
	got, err := deployWith(host, true)
	if err != nil {
		t.Fatalf("a converged status-4 switch must pass, got %v", err)
	}
	if n := strings.Count(strings.Join(host.runs, "\n"), "is-system-running"); n != settleSamples {
		t.Errorf("expected %d settle samples, got %d", settleSamples, n)
	}
	if len(got["warn"]) == 0 || !strings.Contains(got["warn"][0], "authentik-worker.service postgres-netbird-5432-proxy.service") {
		t.Errorf("expected the warning to name the units that failed during the switch, got %v", got["warn"])
	}
}

func TestDeployStatus4CrashLoopFails(t *testing.T) {
	// Nothing shows as failed yet, but a service sits in auto-restart: a crash
	// loop that has not hit its start limit. Must not pass.
	host := &fakeHost{
		switchJournal: switched(4),
		outputs:       onTarget(),
		probeOut:      "verdict=real reason=no-failed-units",
		settleOut:     "state=running\nfailed=\nrestarting=website.service",
	}
	_, err := deployWith(host, true)
	if exitCode(err) != 70 || !strings.Contains(err.Error(), "auto-restart: website.service") {
		t.Fatalf("expected a crash-loop failure, got %v", err)
	}
}

func TestDeployStatus4DegradedFails(t *testing.T) {
	host := &fakeHost{
		switchJournal: switched(4),
		outputs:       onTarget(),
		probeOut:      "verdict=real reason=no-failed-units",
		settleOut:     "state=degraded\nfailed=\nrestarting=",
	}
	_, err := deployWith(host, true)
	if exitCode(err) != 70 || !strings.Contains(err.Error(), `"degraded"`) {
		t.Fatalf("expected a degraded failure, got %v", err)
	}
}

func TestDeployStatus4UnprobeableHostFails(t *testing.T) {
	// The failed-unit probe could not run (no verdict): can't verify => real.
	host := &fakeHost{switchJournal: switched(4), outputs: onTarget()}
	_, err := deployWith(host, true)
	if exitCode(err) != 70 || !strings.Contains(err.Error(), "could not be checked") {
		t.Fatalf("expected an unverifiable failure, got %v", err)
	}
}

func TestParseSwitchJournalKeepsOnlyTheLastSwitch(t *testing.T) {
	out := strings.Join([]string{
		"switching to system configuration /nix/store/old",
		"switching to system configuration /nix/store/old failed (status 4)",
		"switching to system configuration /nix/store/new",
		"finished switching to system configuration /nix/store/old", // not ours
		"active=active",
	}, "\n")
	st := parseSwitchJournal(out)
	if st.Target != "/nix/store/new" || st.Done || !st.Running {
		t.Fatalf("expected the later, still-running switch to /nix/store/new, got %+v", st)
	}
}

func TestDeployBackupFirstBacksUpBeforeSwitching(t *testing.T) {
	host := &fakeHost{outputs: map[string]string{"systemctl cat portablevps-backup.service": "present"}}
	runner := &recordRunner{}
	var order []string
	host2 := &orderHost{fakeHost: host, order: &order}
	err := Deploy(DeployEnv{RepoRoot: "/repo", Runner: &orderRunner{recordRunner: runner, order: &order}, Host: host2, BackupFirst: true, Sleep: noSleep}, "web", "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	backup, rebuild := -1, -1
	for i, step := range order {
		if strings.Contains(step, "systemctl start portablevps-backup.service") && backup < 0 {
			backup = i
		}
		if strings.Contains(step, "nixos-rebuild") && rebuild < 0 {
			rebuild = i
		}
	}
	if backup < 0 || rebuild < 0 || backup > rebuild {
		t.Fatalf("expected the backup before nixos-rebuild, got %v", order)
	}
}

func TestDeployBackupFirstRefusesHostWithoutBackups(t *testing.T) {
	host := &fakeHost{outputs: map[string]string{"systemctl cat portablevps-backup.service": "absent"}}
	runner := &recordRunner{}
	err := Deploy(DeployEnv{RepoRoot: "/repo", Runner: runner, Host: host, BackupFirst: true}, "web", "1.2.3.4")
	if exitCode(err) != 78 || !strings.Contains(err.Error(), "no portablevps-backup.service") {
		t.Fatalf("expected exit 78 for a host without backups, got %v", err)
	}
	if runner.sawContaining("nixos-rebuild") {
		t.Error("must not switch when the requested backup cannot be taken")
	}
}

func TestDeployBackupFirstAbortsWhenBackupFails(t *testing.T) {
	host := &fakeHost{
		outputs:        map[string]string{"systemctl cat portablevps-backup.service": "present"},
		failContaining: "systemctl start portablevps-backup.service",
	}
	runner := &recordRunner{}
	err := Deploy(DeployEnv{RepoRoot: "/repo", Runner: runner, Host: host, BackupFirst: true}, "web", "1.2.3.4")
	if exitCode(err) != 70 || !strings.Contains(err.Error(), "nothing was deployed") {
		t.Fatalf("expected exit 70 when the backup fails, got %v", err)
	}
	if runner.sawContaining("nixos-rebuild") {
		t.Error("must not switch after a failed backup")
	}
}

func TestDeployBackupFirstRejectsDryRun(t *testing.T) {
	host := &fakeHost{}
	runner := &recordRunner{}
	err := Deploy(DeployEnv{RepoRoot: "/repo", Runner: runner, Host: host, BackupFirst: true, DryRun: true}, "web", "1.2.3.4")
	if exitCode(err) != 64 {
		t.Fatalf("expected exit 64 for --backup-first with --dry-run, got %v", err)
	}
	if len(host.runs) != 0 || len(runner.calls) != 0 {
		t.Errorf("must not touch the host, got runs=%v calls=%v", host.runs, runner.calls)
	}
}

// orderHost and orderRunner record host commands and local commands in one
// sequence, to assert ordering across the two.
type orderHost struct {
	*fakeHost
	order *[]string
}

func (h *orderHost) Run(host, command string) (string, error) {
	*h.order = append(*h.order, command)
	return h.fakeHost.Run(host, command)
}

type orderRunner struct {
	*recordRunner
	order *[]string
}

func (r *orderRunner) Stream(dir string, env map[string]string, name string, args ...string) error {
	*r.order = append(*r.order, name+" "+strings.Join(args, " "))
	return r.recordRunner.Stream(dir, env, name, args...)
}
