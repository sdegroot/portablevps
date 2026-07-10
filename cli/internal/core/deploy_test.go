package core

import (
	"strings"
	"testing"
)

type fakeHost struct {
	runs     []string
	failTrue bool
}

func (f *fakeHost) Run(host, command string) (string, error) {
	f.runs = append(f.runs, command)
	if command == "true" && f.failTrue {
		return "", &DeployError{Code: 1, Msg: "unreachable"}
	}
	return "", nil
}
func (f *fakeHost) WaitReady(string) error { return nil }
func (f *fakeHost) NixSSHOpts() string     { return "-i key -p 22" }

type recordRunner struct{ calls [][]string }

func (r *recordRunner) Stream(_ string, env map[string]string, name string, args ...string) error {
	call := append([]string{name}, args...)
	if env["NIX_SSHOPTS"] != "" {
		call = append(call, "NIX_SSHOPTS="+env["NIX_SSHOPTS"])
	}
	r.calls = append(r.calls, call)
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
