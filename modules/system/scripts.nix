# Packages repository maintenance and disaster recovery scripts into the NixOS system path.
{ pkgs, ... }:

let
  scriptPackage = pkgs.runCommand "portablevps-dr-scripts" { } ''
    mkdir -p "$out/bin"
    cp ${../../scripts/lib/runtime-env.sh} "$out/bin/runtime-env.sh"
    cp ${../../scripts/create-zpool.sh} "$out/bin/create-zpool.sh"
    cp ${../../scripts/init-backup-repo.sh} "$out/bin/init-backup-repo.sh"
    cp ${../../scripts/backup.sh} "$out/bin/backup.sh"
    cp ${../../scripts/backup-status.sh} "$out/bin/backup-status.sh"
    cp ${../../scripts/restore.sh} "$out/bin/restore.sh"
    cp ${../../scripts/insert-test-data.sh} "$out/bin/insert-test-data.sh"
    cp ${../../scripts/verify-test-data.sh} "$out/bin/verify-test-data.sh"
    cp ${../../scripts/snapshot.sh} "$out/bin/snapshot.sh"
    cp ${../../scripts/rollback-postgres.sh} "$out/bin/rollback-postgres.sh"
    cp ${../../scripts/qemu-create-vm.sh} "$out/bin/qemu-create-vm.sh"
    cp ${../../scripts/qemu-boot-vm.sh} "$out/bin/qemu-boot-vm.sh"
    cp ${../../scripts/qemu-destroy-vm.sh} "$out/bin/qemu-destroy-vm.sh"
    cp ${../../scripts/qemu-list-vms.sh} "$out/bin/qemu-list-vms.sh"
    cp ${../../scripts/qemu-validate.sh} "$out/bin/qemu-validate.sh"
    cp ${../../scripts/nixos-install-qemu-guest.sh} "$out/bin/nixos-install-qemu-guest.sh"
    chmod +x "$out"/bin/*.sh
  '';
in
{
  environment.systemPackages = [ scriptPackage ];
}
