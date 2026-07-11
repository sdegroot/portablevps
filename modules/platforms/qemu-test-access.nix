# Adds the local QEMU test SSH key for noninteractive disaster recovery
# validation, and opens SSH on the guest firewall. Base is key-only and opens no
# ports by default (secure-by-default); the QEMU harness reaches the guest over a
# host->guest :22 forward, so a local test VM opts back into an open SSH port
# here (still key-only — password auth stays off).
{ lib, ... }:

{
  users.users.admin.openssh.authorizedKeys.keyFiles = [
    ../../keys/qemu-test.pub
  ];

  portablevps.base.allowedTCPPorts = lib.mkDefault [ 22 ];
}
