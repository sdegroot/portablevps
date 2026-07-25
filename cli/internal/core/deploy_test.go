package core

import (
	"strings"
	"testing"
)

type fakeHost struct {
	runs                 []string
	failTrue             bool
	failRestoreModeCheck bool
	journal              string // returned for journalctl commands
	probeOut             string // returned for the failed-unit health probe
	outputs              map[string]string
	failContaining       string
	machineIDs           map[string]string
}

func (f *fakeHost) Run(host, command string) (string, error) {
	f.runs = append(f.runs, command)
	if command == "true" && f.failTrue {
		return "", &DeployError{Code: 1, Msg: "unreachable"}
	}
	if f.failRestoreModeCheck && strings.Contains(command, "restore-mode") {
		return "", &DeployError{Code: 1, Msg: "not in restore mode"}
	}
	if f.failContaining != "" && strings.Contains(command, f.failContaining) {
		return "", &DeployError{Code: 1, Msg: "command failed"}
	}
	if command == "cat /etc/machine-id" {
		if id := f.machineIDs[host]; id != "" {
			return id, nil
		}
		return "machine-" + host, nil
	}
	if strings.Contains(command, "journalctl") {
		return f.journal, nil
	}
	if strings.Contains(command, "--state=failed") {
		return f.probeOut, nil
	}
	for match, output := range f.outputs {
		if strings.Contains(command, match) {
			return output, nil
		}
	}
	return "", nil
}
func (f *fakeHost) RunInput(host, command, _ string) (string, error) {
	f.runs = append(f.runs, command)
	return "", nil
}
func (f *fakeHost) WaitReady(string) error { return nil }
func (f *fakeHost) NixSSHOpts() string     { return "-i key -p 22" }

type recordRunner struct {
	calls [][]string
	fail  bool
}

func (r *recordRunner) Stream(_ string, env map[string]string, name string, args ...string) error {
	call := append([]string{name}, args...)
	if env["NIX_SSHOPTS"] != "" {
		call = append(call, "NIX_SSHOPTS="+env["NIX_SSHOPTS"])
	}
	r.calls = append(r.calls, call)
	if r.fail {
		return &DeployError{Code: 1, Msg: "switch failed"}
	}
	return nil
}

func (r *recordRunner) sawContaining(sub string) bool {
	for _, c := range r.calls {
		if strings.Contains(strings.Join(c, " "), sub) {
			return true
		}
	}
	return false
}

func TestDeployRunsSwitchAndReportsSuccess(t *testing.T) {
	host := &fakeHost{}
	runner := &recordRunner{}
	err := Deploy(DeployEnv{RepoRoot: "/repo", Runner: runner, Host: host}, "web", "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if !runner.sawContaining("nixos-rebuild") || !runner.sawContaining("--flake /repo#web") {
		t.Errorf("expected nixos-rebuild switch of .#web, got %v", runner.calls)
	}
	if !runner.sawContaining("NIX_SSHOPTS=-i key") {
		t.Errorf("expected NIX_SSHOPTS threaded through, got %v", runner.calls)
	}
	// success reporter unit started
	joined := strings.Join(host.runs, " | ")
	if !strings.Contains(joined, "deploy-report-success") {
		t.Errorf("expected success report, got %q", joined)
	}
}

func TestDeployFailedSwitchSurfacesBlueGreenHint(t *testing.T) {
	host := &fakeHost{journal: "blue-green(website): idle green failed health check; kept blue on old image"}
	runner := &recordRunner{fail: true}
	var infos []string
	env := DeployEnv{
		RepoRoot: "/repo", Runner: runner, Host: host,
		Report: func(_, status, msg string) {
			if status == "info" {
				infos = append(infos, msg)
			}
		},
	}
	err := Deploy(env, "web", "1.2.3.4")
	if e, ok := err.(*DeployError); !ok || e.ExitCode() != 70 {
		t.Fatalf("expected exit 70 on a failed switch, got %v", err)
	}
	joined := strings.Join(host.runs, " | ")
	if !strings.Contains(joined, "deploy-report-failure") {
		t.Errorf("expected failure report, got %q", joined)
	}
	if !strings.Contains(joined, "journalctl -u '*-bluegreen.service'") {
		t.Errorf("expected blue-green journal fetch on failure, got %q", joined)
	}
	if !strings.Contains(strings.Join(infos, " "), "idle green failed health check") {
		t.Errorf("expected reconcile log surfaced as an info report, got %v", infos)
	}
}

func TestDeployTreatsHealthcheckFalsePositiveAsSuccess(t *testing.T) {
	// switch-to-configuration exited non-zero, but the only failed unit is a
	// podman healthcheck transient whose container came up healthy.
	host := &fakeHost{probeOut: "healthcheck-transient-ok unit=abc123def456-9f.service cid=abc123def456\nverdict=benign units= abc123def456-9f.service"}
	runner := &recordRunner{fail: true}
	var warns, oks []string
	env := DeployEnv{
		RepoRoot: "/repo", Runner: runner, Host: host,
		Report: func(_, status, msg string) {
			switch status {
			case "warn":
				warns = append(warns, msg)
			case "ok":
				oks = append(oks, msg)
			}
		},
	}
	if err := Deploy(env, "web", "1.2.3.4"); err != nil {
		t.Fatalf("benign healthcheck false-positive must not fail the deploy, got %v", err)
	}
	joined := strings.Join(host.runs, " | ")
	if !strings.Contains(joined, "reset-failed abc123def456-9f.service") {
		t.Errorf("expected the stale transient unit to be reset-failed, got %q", joined)
	}
	if !strings.Contains(joined, "deploy-report-success") {
		t.Errorf("expected a SUCCESS report (not failure) for a benign false-positive, got %q", joined)
	}
	if strings.Contains(joined, "deploy-report-failure") {
		t.Errorf("must not fire the failure reporter (DeployFailing alert) on a benign false-positive, got %q", joined)
	}
	if len(warns) == 0 {
		t.Error("expected a warning explaining the tolerated false-positive")
	}
}

func TestDeployRealUnitFailureStillFails(t *testing.T) {
	// A non-healthcheck unit failed to start: a genuine failure, must not be
	// swallowed by the healthcheck-tolerance path.
	host := &fakeHost{
		probeOut: "verdict=real reason=non-healthcheck-unit unit=grafana.service",
		journal:  "blue-green(website): idle green failed health check; kept blue on old image",
	}
	runner := &recordRunner{fail: true}
	err := Deploy(DeployEnv{RepoRoot: "/repo", Runner: runner, Host: host}, "web", "1.2.3.4")
	if e, ok := err.(*DeployError); !ok || e.ExitCode() != 70 {
		t.Fatalf("a real unit failure must still exit 70, got %v", err)
	}
	joined := strings.Join(host.runs, " | ")
	if !strings.Contains(joined, "deploy-report-failure") {
		t.Errorf("expected the failure reporter on a real failure, got %q", joined)
	}
	if strings.Contains(joined, "reset-failed") {
		t.Errorf("must not reset-failed anything on a real failure, got %q", joined)
	}
}

func TestDeployFailsFastOnUnreachableHost(t *testing.T) {
	host := &fakeHost{failTrue: true}
	runner := &recordRunner{}
	err := Deploy(DeployEnv{RepoRoot: "/repo", Runner: runner, Host: host}, "web", "1.2.3.4")
	var de *DeployError
	if err == nil {
		t.Fatal("expected failure on unreachable host")
	}
	if e, ok := err.(*DeployError); ok {
		de = e
	}
	if de == nil || de.ExitCode() != 71 {
		t.Fatalf("expected exit 71, got %v", err)
	}
	if runner.sawContaining("nixos-rebuild") {
		t.Error("must not attempt switch when the host is unreachable")
	}
}
