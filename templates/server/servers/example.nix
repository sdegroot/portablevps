# An example logical server. Copy this file per server (the file name is the
# server name) and adjust the placement, identity, and routing.
{ ... }:

let
  name = "example";
in
{
  inherit name;

  # Reusable server shape. "single-instance-app" is a PostgreSQL app server
  # with coordinated backup/restore; or set this to a path for a custom profile.
  profile = "single-instance-app";

  # Which provider this server currently runs on. Provider metadata comes from
  # portablevps' built-in providers/ (or this repo's providers/ if present).
  placement = {
    provider = "hetzner";
  };

  # Metadata consumed by both Nix and the portablevps CLI.
  info = {
    inherit name;
    provider = "hetzner";
    placement = { provider = "hetzner"; };
    hostname = name;
    netbirdName = name;
    # Per-server restic repository prefix inside your backup bucket.
    backupRepository = "s3:https://s3.example.com/my-backups/servers/${name}/restic";
  };

  # NixOS configuration for this specific server.
  module = { lib, ... }: {
    networking.hostName = lib.mkDefault name;

    # Your provisioned cloud admin key and encrypted secrets file.
    portablevps.base.adminAuthorizedKeyFiles = [ ../keys/cloud-admin.pub ];
    portablevps.secrets.file = ../secrets/secrets.yaml;

    portablevps.server = {
      inherit name;
      provider = "hetzner";
      backupRepository = "s3:https://s3.example.com/my-backups/servers/${name}/restic";
    };

    # Mesh VPN peer name. Set backend = "tailscale" to use Tailscale instead.
    portablevps.network.name = lib.mkDefault name;

    # Declare application routes here (see portablevps' proxy documentation):
    # portablevps.proxy = {
    #   enable = true;
    #   acme = { email = "ops@example.com"; dnsProvider = "desec"; };
    #   http.services.app = { domain = "app.example.com"; upstream = "http://127.0.0.1:3000"; };
    # };
  };
}
