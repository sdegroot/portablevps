# Reusable Discourse application server shape.
{ ... }:

{
  imports = [
    ../runtime/docker.nix
    ../services/container-state.nix
    ../system/backups.nix
    ../networking/service-exposure.nix
    ../networking/proxy.nix
    ../../apps/discourse
  ];
}
