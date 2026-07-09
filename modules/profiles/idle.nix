# Reusable shape for an intentionally idle cloud host. It keeps the shared cloud
# base (SSH, NetBird, break-glass access, telemetry, scripts, sops) and enough
# runtime plumbing to manage old containers after a service move, but declares no
# applications, proxy routes, PostgreSQL, or backup components.
{ ... }:

{
  imports = [
    ../runtime/podman.nix
    ../networking/service-exposure.nix
    # Declares portablevps.backups.* for core modules. With no registered
    # components the backup timers stay disarmed.
    ../system/backups.nix
  ];
}
