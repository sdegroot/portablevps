# Enables Docker for roles that need Docker-native tooling.
{ lib, ... }:

{
  virtualisation.docker = {
    enable = true;
    # `--all`: like Podman, Docker never counts a digest-pulled image as
    # dangling, so a prune without it frees nothing (see runtime/podman.nix).
    autoPrune = {
      enable = lib.mkDefault true;
      dates = lib.mkDefault "daily";
      flags = lib.mkDefault [ "--all" "--filter" "until=72h" ];
    };
  };
}
