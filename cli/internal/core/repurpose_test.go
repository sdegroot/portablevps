package core

import (
	"strings"
	"testing"
)

// repurposeHost records both plain commands and stdin-fed inputs, so a test can
// assert the age key is shipped over stdin (RunInput) and never as a command.
type repurposeHost struct {
	runs   []string
	inputs map[string]string
}

func (h *repurposeHost) Run(_, cmd string) (string, error) {
	h.runs = append(h.runs, cmd)
	return "", nil
}
func (h *repurposeHost) RunInput(_, cmd, input string) (string, error) {
	h.runs = append(h.runs, cmd)
	if h.inputs == nil {
		h.inputs = map[string]string{}
	}
	h.inputs[cmd] = input
	return "", nil
}
func (h *repurposeHost) WaitReady(string) error { return nil }
func (h *repurposeHost) NixSSHOpts() string     { return "-i key -p 22" }

// TestRepurposeRejectsUnsafeResetPaths guards the destructive `rm -rf` on the
// live host: only clean paths under /data or /var/lib may be reset, and a `..`
// traversal that escapes those roots must be rejected before any command runs.
func TestRepurposeRejectsUnsafeResetPaths(t *testing.T) {
	for _, p := range []string{
		"/etc",
		"/tmp/x",
		"/var/lib/../../etc",    // traversal escaping the allow-listed root
		"/data/../etc",          // traversal escaping the allow-listed root
		"/var/lib/../root/.ssh", // cleans to /root/.ssh
	} {
		host := &repurposeHost{}
		env := RepurposeEnv{RepoRoot: "/repo", Host: host, Stream: &recordRunner{}, AgeKeyMaterial: "AGE-KEY"}
		err := Repurpose(env, RepurposeOpts{Server: "web", Host: "1.2.3.4", ResetPaths: []string{p}})
		if err == nil {
			t.Errorf("expected reset path %q to be rejected", p)
		}
		if len(host.runs) != 0 {
			t.Errorf("reset path %q reached the host before validation: %v", p, host.runs)
		}
	}
}

// TestRepurposeShipsKeyViaStdinAndClearsCleanedPath verifies the happy path: the
// age key goes over stdin (never argv), a valid reset path is cleaned and wiped,
// apps stop before the switch, and the switch runs via the streaming runner.
func TestRepurposeShipsKeyViaStdinAndClearsCleanedPath(t *testing.T) {
	host := &repurposeHost{}
	runner := &recordRunner{}
	env := RepurposeEnv{RepoRoot: "/repo", Host: host, Stream: runner, AgeKeyMaterial: "AGE-SECRET"}
	// a valid but non-canonical path must be cleaned, then wiped.
	err := Repurpose(env, RepurposeOpts{Server: "web", Host: "1.2.3.4", ResetPaths: []string{"/var/lib/foo/../foo"}})
	if err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(host.runs, "\n")
	// the key value must never appear in a command (argv / process list).
	if strings.Contains(joined, "AGE-SECRET") {
		t.Error("age key leaked into a host command; it must be fed over stdin")
	}
	// the key must have been shipped via a stdin-fed command.
	shipped := false
	for _, in := range host.inputs {
		if strings.TrimSpace(in) == "AGE-SECRET" {
			shipped = true
		}
	}
	if !shipped {
		t.Error("age key was not shipped via RunInput/stdin")
	}
	// apps stopped, and the cleaned reset path (not the raw one) is wiped.
	if !strings.Contains(joined, "systemctl stop apps.target") {
		t.Error("apps were not stopped before switch")
	}
	if !strings.Contains(joined, "/var/lib/foo") || strings.Contains(joined, "foo/../foo") {
		t.Errorf("reset should target the cleaned path /var/lib/foo, got: %v", host.runs)
	}
	if !runner.sawContaining("switch") {
		t.Error("nixos-rebuild switch was not run via the streaming runner")
	}
}
