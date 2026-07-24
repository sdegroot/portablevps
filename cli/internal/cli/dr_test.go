package cli

import (
	"testing"

	"github.com/epistola-app/portablevps/internal/config"
)

func TestResolveDRModeDefaultsToQEMU(t *testing.T) {
	mode, err := resolveDRMode(drFlags{})
	if err != nil {
		t.Fatal(err)
	}
	if mode != "qemu" {
		t.Fatalf("mode = %q, want qemu", mode)
	}
}

func TestResolveDRModeInfersRemoteFromTargets(t *testing.T) {
	mode, err := resolveDRMode(drFlags{sourceServer: "src"})
	if err != nil {
		t.Fatal(err)
	}
	if mode != "remote" {
		t.Fatalf("mode = %q, want remote", mode)
	}
}

func TestResolveDRModeRejectsRemoteTargetsWithQEMU(t *testing.T) {
	_, err := resolveDRMode(drFlags{mode: "qemu", restoreHost: "restore.example"})
	if err == nil {
		t.Fatal("expected remote target flags to be rejected with qemu mode")
	}
}

func TestResolveDRRemoteHostsFromServerNames(t *testing.T) {
	ctx := &config.Context{MeshDomain: "example.internal"}
	source, restore, err := resolveDRRemoteHosts(ctx, drFlags{
		sourceServer:  "src",
		restoreServer: "dst",
	})
	if err != nil {
		t.Fatal(err)
	}
	if source != "src.example.internal" {
		t.Fatalf("source = %q", source)
	}
	if restore != "dst.example.internal" {
		t.Fatalf("restore = %q", restore)
	}
}

func TestResolveDRRemoteHostRejectsAmbiguousTarget(t *testing.T) {
	_, err := resolveDRRemoteHost(&config.Context{MeshDomain: "example.internal"}, "source", "src", "1.2.3.4")
	if err == nil {
		t.Fatal("expected both source server and source host to be rejected")
	}
}
