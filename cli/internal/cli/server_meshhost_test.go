package cli

import (
	"testing"

	"github.com/epistola-app/portablevps/internal/config"
)

// TestDefaultMeshHostUsesMeshDomain locks the admin-address default to the mesh
// PEER domain (network.mesh_domain), NOT the service dns_zone. These are
// distinct: dns_zone holds published app hostnames (int.epistola.io) while the
// operator reaches peers over NetBird at <server>.epistola.int. Deriving the
// SSH host from dns_zone produced an unresolvable name — this test guards the
// regression.
func TestDefaultMeshHostUsesMeshDomain(t *testing.T) {
	ctx := &config.Context{
		DNSZone:    "int.epistola.io",
		MeshDomain: "epistola.int",
	}
	got := defaultMeshHost("hetzner-nbg1-20260708a", ctx)
	if want := "hetzner-nbg1-20260708a.epistola.int"; got != want {
		t.Errorf("defaultMeshHost = %q, want %q (must use mesh_domain, not dns_zone)", got, want)
	}
}

// TestDefaultMeshHostEmptyWithoutMeshDomain confirms we return "" (forcing an
// explicit --host) rather than falling back to dns_zone, which would silently
// build a wrong, unresolvable admin address.
func TestDefaultMeshHostEmptyWithoutMeshDomain(t *testing.T) {
	ctx := &config.Context{DNSZone: "int.epistola.io"}
	if got := defaultMeshHost("web-1", ctx); got != "" {
		t.Errorf("defaultMeshHost with no mesh_domain = %q, want empty", got)
	}
}
