# Dedicated Forgejo Actions runner host profile.
#
# Runners execute repository-controlled code, so this profile deliberately avoids
# PostgreSQL and service backups. State is registration/work cache only; the
# Forgejo service remains a separate single-instance-app host.
{ ... }:

{
  imports = [
    ../runtime/podman.nix
    ../networking/service-exposure.nix
    ../system/backups.nix
    ../../apps/forgejo-runner
  ];
}
