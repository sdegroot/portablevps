package cli

import "testing"

func TestSplitSSHArgsWithDash(t *testing.T) {
	server, remote := splitSSHArgs([]string{"web", "sudo", "systemctl", "status"}, 1, false)
	if len(server) != 1 || server[0] != "web" {
		t.Fatalf("server args = %#v", server)
	}
	want := []string{"sudo", "systemctl", "status"}
	if !sameStrings(remote, want) {
		t.Fatalf("remote args = %#v, want %#v", remote, want)
	}
}

func TestSplitSSHArgsWithServerFlag(t *testing.T) {
	server, remote := splitSSHArgs([]string{"uptime"}, 0, true)
	if len(server) != 0 {
		t.Fatalf("server args = %#v, want empty", server)
	}
	want := []string{"uptime"}
	if !sameStrings(remote, want) {
		t.Fatalf("remote args = %#v, want %#v", remote, want)
	}
}

func TestSplitSSHArgsAllowsBareInteractiveServer(t *testing.T) {
	server, remote := splitSSHArgs([]string{"web"}, -1, false)
	if len(server) != 1 || server[0] != "web" {
		t.Fatalf("server args = %#v", server)
	}
	if len(remote) != 0 {
		t.Fatalf("remote args = %#v, want empty", remote)
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
