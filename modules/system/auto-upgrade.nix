# Pull-based self-upgrade: a box rebuilds ITSELF from the committed flake on a
# timer, instead of an operator pushing with `cloud:deploy`.
#
# Wraps the built-in `system.autoUpgrade` (so we inherit its nixos-upgrade
# service/timer, randomized delay, and flake handling). The flake target defaults
# to the box's hostname, which equals its nixosConfigurations attr name, so
# `<flake>#<hostname>` resolves to this box's own config.
#
# Why pull over a CI push: keeps everything git-pinned and declarative (unified
# Nix rollback covers infra AND the app image pin), and puts NO operator SSH key
# or age key in CI — the box needs only read access to the infra repo (a
# read-only deploy key in its own sops). A new website release becomes a commit
# that bumps the pinned image; the box applies it on the next tick and the
# restart-on-change step (runtime/podman.nix) rolls the container.
#
# Safety posture: a failed BUILD never switches (the box stays put); a
# builds-fine-but-breaks change relies on the image's HEALTHCHECK + a `git revert`
# (there is no automatic runtime rollback — see docs/adr/0003). Disabled during a
# disaster-recovery restore (both at build time and via the runtime marker).
{ lib, config, pkgs, ... }:

let
  cfg = config.portablevps.autoUpgrade;
in
{
  options.portablevps.autoUpgrade = {
    enable = lib.mkEnableOption "pull-based self-upgrade from the committed flake";

    flake = lib.mkOption {
      type = lib.types.str;
      example = "git+ssh://git@github.com/epistola-app/epistola-nix-infra?dir=epistola";
      description = ''
        Flake reference the box rebuilds itself from. `nixos-rebuild` appends
        `#<hostname>` automatically, so point this at the flake root (no fragment).
        Use a real remote URL (git+ssh / github:), not a local path input.
      '';
    };

    deployKeySecret = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "github/deploy-key";
      description = ''
        Optional sops key holding a read-only SSH private key used to fetch the
        (private) flake repo over git+ssh. When set, the nixos-upgrade service
        fetches with it via GIT_SSH_COMMAND. Leave null for a public repo or when
        credentials are provided some other way.
      '';
    };

    dates = lib.mkOption {
      type = lib.types.str;
      default = "*:0/30";
      description = "systemd OnCalendar spec for how often to pull + rebuild (default: every 30 min).";
    };

    randomizedDelaySec = lib.mkOption {
      type = lib.types.str;
      default = "5min";
      description = "Random delay added to each run so a fleet doesn't hit the git host in lockstep.";
    };
  };

  # Disabled during restore: gated at build time on portablevps.restoreMode (the
  # -restore config variant), and at runtime via the marker file so a live
  # disaster-recovery restore is never fought by a self-upgrade.
  config = lib.mkIf (cfg.enable && !config.portablevps.restoreMode) (lib.mkMerge [
    {
      system.autoUpgrade = {
        enable = true;
        flake = cfg.flake;
        dates = cfg.dates;
        randomizedDelaySec = cfg.randomizedDelaySec;
        # No unattended reboots: kernel/bootloader changes wait for a deliberate
        # deploy. The website use case only bumps a container image pin.
        allowReboot = false;
      };

      systemd.services.nixos-upgrade.unitConfig.ConditionPathExists =
        "!/run/portablevps/restore-mode";
    }

    (lib.mkIf (cfg.deployKeySecret != null) {
      sops.secrets.${cfg.deployKeySecret} = {
        mode = "0400";
        owner = "root";
      };

      systemd.services.nixos-upgrade.serviceConfig.Environment = [
        ("GIT_SSH_COMMAND=${pkgs.openssh}/bin/ssh"
          + " -i ${config.sops.secrets.${cfg.deployKeySecret}.path}"
          + " -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new")
      ];
    })
  ]);
}
