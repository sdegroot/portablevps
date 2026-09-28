package cli

import (
	"slices"
	"strings"
	"testing"
)

func TestPasswordBootstrapNeverOffersAgentKeys(t *testing.T) {
	name, args := bootstrapCommand(true, "", []string{"-p", "22"}, "root@203.0.113.7", "true")
	if name != "sshpass" {
		t.Fatalf("password bootstrap must use sshpass, got %q", name)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"PubkeyAuthentication=no",
		"IdentityAgent=none",
		"PreferredAuthentications=password,keyboard-interactive",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("password bootstrap is missing -o %s: %s", want, joined)
		}
	}
	// ssh takes the first value of an option, so ours must precede the caller's.
	if slices.Index(args, "PubkeyAuthentication=no") > slices.Index(args, "-p") {
		t.Errorf("password-only options must come before the other ssh options: %s", joined)
	}
	if args[len(args)-2] != "root@203.0.113.7" || args[len(args)-1] != "true" {
		t.Errorf("target and remote command must come last: %s", joined)
	}
}

func TestKeyAndAgentBootstrapKeepPublicKeyAuth(t *testing.T) {
	for _, tc := range []struct {
		name       string
		initialKey string
	}{{"initial key", "/keys/admin"}, {"agent", ""}} {
		_, args := bootstrapCommand(false, tc.initialKey, []string{"-p", "22"}, "root@203.0.113.7", "true")
		if strings.Contains(strings.Join(args, " "), "PubkeyAuthentication=no") {
			t.Errorf("%s bootstrap must not disable public-key auth: %v", tc.name, args)
		}
	}
	_, args := bootstrapCommand(false, "/keys/admin", nil, "root@h", "true")
	if !slices.Contains(args, "/keys/admin") || !slices.Contains(args, "IdentitiesOnly=yes") {
		t.Errorf("initial-key bootstrap must pin that key: %v", args)
	}
}
