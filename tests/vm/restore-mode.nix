# Hermetic NixOS VM test for the restore-mode safety gate: application services
# (anything in apps.target) must not start while a host is in restore mode, and
# must start once it is switched to normal mode. This is the invariant that
# keeps a fresh host from starting services before its data is restored.
#
# This test uses a dummy apps.target service instead of the PostgreSQL
# container so it stays fully offline and image-free; the full backup/restore
# proof lives in tests/test-disaster-recovery.sh (two real VMs + restic).
{ pkgs }:

let
  restoreModeModule = ../../modules/system/restore-mode.nix;

  dummyApp = { lib, ... }: {
    # A stand-in application unit wired the same way real services are:
    # part of apps.target and gated on the restore-mode runtime marker.
    systemd.services.dummy-app = {
      description = "Dummy application service";
      wantedBy = [ "apps.target" ];
      partOf = [ "apps.target" ];
      unitConfig.ConditionPathExists = "!/run/portablevps/restore-mode";
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = "${pkgs.coreutils}/bin/true";
      };
    };
  };
in
pkgs.testers.runNixOSTest {
  name = "portablevps-restore-mode";

  nodes = {
    normal = { ... }: {
      imports = [ restoreModeModule dummyApp ];
      portablevps.restoreMode = false;
    };

    restore = { ... }: {
      imports = [ restoreModeModule dummyApp ];
      portablevps.restoreMode = true;
    };
  };

  testScript = ''
    start_all()

    with subtest("normal mode starts application services"):
        normal.wait_for_unit("multi-user.target")
        normal.wait_for_unit("apps.target")
        normal.succeed("systemctl is-active dummy-app.service")
        normal.succeed("test -f /etc/portablevps/restore-mode")
        normal.succeed("grep -q false /etc/portablevps/restore-mode")

    with subtest("restore mode keeps application services stopped"):
        restore.wait_for_unit("multi-user.target")
        restore.succeed("test -f /run/portablevps/restore-mode")
        restore.succeed("test -f /etc/portablevps/restore-mode")
        restore.succeed("grep -q true /etc/portablevps/restore-mode")
        # apps.target must not be wanted by multi-user in restore mode, so the
        # dummy app must be inactive.
        restore.fail("systemctl is-active apps.target")
        restore.fail("systemctl is-active dummy-app.service")
  '';
}
