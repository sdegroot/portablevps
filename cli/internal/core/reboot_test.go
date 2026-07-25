package core

import (
	"errors"
	"strings"
	"testing"
	"time"
)

type rebootHost struct {
	bootIDs       []string
	bootIDCalls   int
	rebootErr     error
	bootedSystem  string
	currentSystem string
	failedUnits   string
	systemStates  []string
	stateCalls    int
	runs          []string
}

func (h *rebootHost) Run(_ string, command string) (string, error) {
	h.runs = append(h.runs, command)
	switch command {
	case "cat " + bootIDPath:
		if h.bootIDCalls >= len(h.bootIDs) {
			h.bootIDCalls++
			return "", errors.New("unreachable")
		}
		out := h.bootIDs[h.bootIDCalls]
		h.bootIDCalls++
		if out == "" {
			return "", errors.New("unreachable")
		}
		return out, nil
	case "sudo systemctl reboot":
		return "", h.rebootErr
	case "readlink -f /run/booted-system":
		return h.bootedSystem, nil
	case "readlink -f /run/current-system":
		return h.currentSystem, nil
	case "sudo systemctl is-system-running 2>/dev/null || true":
		if len(h.systemStates) == 0 {
			return "running", nil
		}
		if h.stateCalls >= len(h.systemStates) {
			return h.systemStates[len(h.systemStates)-1], nil
		}
		out := h.systemStates[h.stateCalls]
		h.stateCalls++
		return out, nil
	case "sudo systemctl list-units --type=service --state=failed --plain --no-legend":
		return h.failedUnits, nil
	default:
		return "", nil
	}
}

func (h *rebootHost) RunInput(string, string, string) (string, error) { return "", nil }
func (h *rebootHost) WaitReady(string) error                          { return nil }
func (h *rebootHost) NixSSHOpts() string                              { return "" }

func noSleep(time.Duration) {}

func TestRebootWaitsForChangedBootIDAndVerifiesGeneration(t *testing.T) {
	host := &rebootHost{
		bootIDs:       []string{"old", "old", "", "new"},
		bootedSystem:  "/nix/store/new-system",
		currentSystem: "/nix/store/new-system",
	}
	got, err := Reboot(RebootEnv{Host: host, Attempts: 4, Sleep: noSleep}, "web.example")
	if err != nil {
		t.Fatal(err)
	}
	if got.BootID != "new" || got.System != "/nix/store/new-system" {
		t.Fatalf("unexpected reboot result: %+v", got)
	}
	if !strings.Contains(strings.Join(host.runs, " | "), "sudo systemctl reboot") {
		t.Fatalf("reboot command was not issued: %v", host.runs)
	}
}

func TestRebootAcceptsSSHDisconnectWhenNewBootIsObserved(t *testing.T) {
	host := &rebootHost{
		bootIDs:       []string{"old", "", "new"},
		rebootErr:     errors.New("connection closed"),
		bootedSystem:  "/nix/store/new-system",
		currentSystem: "/nix/store/new-system",
	}
	if _, err := Reboot(RebootEnv{Host: host, Attempts: 3, Sleep: noSleep}, "web.example"); err != nil {
		t.Fatalf("changed boot ID must prove reboot despite SSH disconnect: %v", err)
	}
}

func TestRebootWaitsForSystemToSettle(t *testing.T) {
	host := &rebootHost{
		bootIDs:       []string{"old", "new"},
		bootedSystem:  "/nix/store/new-system",
		currentSystem: "/nix/store/new-system",
		systemStates:  []string{"starting", "running"},
	}
	if _, err := Reboot(RebootEnv{Host: host, Attempts: 2, Sleep: noSleep}, "web.example"); err != nil {
		t.Fatal(err)
	}
	if host.stateCalls != 2 {
		t.Fatalf("expected system state to be polled twice, got %d", host.stateCalls)
	}
}

func TestRebootFailsWhenBootIDNeverChanges(t *testing.T) {
	host := &rebootHost{bootIDs: []string{"old", "old", "old"}}
	_, err := Reboot(RebootEnv{Host: host, Attempts: 2, Sleep: noSleep}, "web.example")
	if err == nil || !strings.Contains(err.Error(), "did not complete a new boot") {
		t.Fatalf("expected reboot timeout, got %v", err)
	}
}

func TestRebootFailsWhenBootedGenerationDoesNotMatch(t *testing.T) {
	host := &rebootHost{
		bootIDs:       []string{"old", "new"},
		bootedSystem:  "/nix/store/old-system",
		currentSystem: "/nix/store/new-system",
	}
	_, err := Reboot(RebootEnv{Host: host, Attempts: 1, Sleep: noSleep}, "web.example")
	if err == nil || !strings.Contains(err.Error(), "booted") {
		t.Fatalf("expected generation mismatch, got %v", err)
	}
}

func TestRebootFailsOnFailedServices(t *testing.T) {
	host := &rebootHost{
		bootIDs:       []string{"old", "new"},
		bootedSystem:  "/nix/store/new-system",
		currentSystem: "/nix/store/new-system",
		failedUnits:   "website.service loaded failed failed Website",
	}
	_, err := Reboot(RebootEnv{Host: host, Attempts: 1, Sleep: noSleep}, "web.example")
	if err == nil || !strings.Contains(err.Error(), "failed services") {
		t.Fatalf("expected failed-service error, got %v", err)
	}
}
