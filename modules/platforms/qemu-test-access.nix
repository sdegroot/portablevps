# Adds the local QEMU test SSH key for noninteractive disaster recovery
# validation, and opens SSH on the guest firewall. Base is key-only and opens no
# ports by default (secure-by-default); the QEMU harness reaches the guest over a
# host->guest :22 forward, so a local test VM opts back into an open SSH port
# here (still key-only — password auth stays off).
{ lib, ... }:

{
  # The QEMU harness reaches the guest over a host->guest :22 forward with this
  # test key. Contribute it through the portablevps admin-key option (which base
  # wires to the admin's authorizedKeys) rather than the low-level option, so the
  # base reachability assertion is satisfied for hosts that carry no other admin
  # key — e.g. the tool's own local-vm DR host. Consumer local-vm hosts already
  # set their cloud-admin key; list definitions merge, so they get both.
  portablevps.base.adminAuthorizedKeyFiles = [ ../../keys/qemu-test.pub ];

  portablevps.base.allowedTCPPorts = lib.mkDefault [ 22 ];

  # The DR harness drives the real backup service explicitly (full, then
  # incremental). A Persistent timer can otherwise fire during
  # `nixos-rebuild switch`, before the harness can stop it, and race that
  # controlled sequence. Keep only automatic scheduling off in disposable
  # QEMU configurations; the production service and all of its hooks remain
  # exactly the same.
  portablevps.backups.scheduling.enable = lib.mkForce false;
}
