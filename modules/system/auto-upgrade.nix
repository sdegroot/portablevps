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
# or age key in CI — the box needs only READ access to the infra repo (a token in
# its own sops). A new website release becomes a commit that bumps the pinned
# image; the box applies it on the next tick and the restart-on-change step
# (runtime/podman.nix) rolls the container.
#
# Repo auth: a fine-grained, read-only token (Contents: Read-only) passed to nix
# as an `access-tokens` entry, used with a `github:`-style flake URL. We use a
# token rather than an SSH deploy key because deploy keys are un-expiring, per-repo
# SSH credentials outside org token governance (and are disabled at the org level
# here) — a fine-grained token is centrally visible, revocable, and expiring. See
# docs/adr/0003.
#
# Safety posture: a failed BUILD never switches (the box stays put and retries on
# the next tick — self-correcting once a good commit lands); a builds-fine-but-
# breaks change relies on the image HEALTHCHECK + a `git revert` (no automatic
# runtime rollback). A persistently-failing upgrade is otherwise silent, so on a
# successful run the telemetry shipper stamps
# `portablevps_autoupgrade_last_success_timestamp_seconds` and the gateway alerts
# when it goes stale (see modules/system/telemetry.nix + apps/monitoring/rules).
# Disabled during a disaster-recovery restore (build-time and via the marker).
{ lib, config, pkgs, ... }:

let
  cfg = config.portablevps.autoUpgrade;
in
{
  options.portablevps.autoUpgrade = {
    enable = lib.mkEnableOption "pull-based self-upgrade from the committed flake";

    flake = lib.mkOption {
      type = lib.types.str;
      example = "github:epistola-app/epistola-nix-infra?dir=epistola";
      description = ''
        Flake reference the box rebuilds itself from. `nixos-rebuild` appends
        `#<hostname>` automatically, so point this at the flake root (no fragment).
        Use a real remote URL (`github:` / `git+https:`), not a local path input.
      '';
    };

    tokenSecret = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "github/repo-token";
      description = ''
        Optional sops key holding a read-only token (for GitHub: a fine-grained PAT
        with `Contents: Read-only` on the infra repo) used to fetch the private
        flake. Passed to nix as an `access-tokens` entry for `tokenHost`. Leave
        null for a public repo.
      '';
    };

    tokenHost = lib.mkOption {
      type = lib.types.str;
      default = "github.com";
      description = "Host the `tokenSecret` authenticates to, as the nix `access-tokens` key.";
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

    (lib.mkIf (cfg.tokenSecret != null) {
      sops.secrets.${cfg.tokenSecret} = { };

      # Render `NIX_CONFIG=access-tokens = <host>=<token>` and hand it to the
      # nixos-upgrade service as an EnvironmentFile, so the token stays in sops
      # (root-only) rather than world-readable /etc/nix/nix.conf.
      sops.templates."portablevps/autoupgrade-token.env" = {
        mode = "0400";
        owner = "root";
        content = "NIX_CONFIG=access-tokens = ${cfg.tokenHost}=${config.sops.placeholder.${cfg.tokenSecret}}";
      };

      systemd.services.nixos-upgrade.serviceConfig.EnvironmentFile =
        config.sops.templates."portablevps/autoupgrade-token.env".path;
    })
  ]);
}
