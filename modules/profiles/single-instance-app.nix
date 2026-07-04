# Defines the reusable single-instance application server shape.
{ lib, serverConfig ? { }, ... }:

let
  netbirdName = serverConfig.netbirdName or serverConfig.hostname or serverConfig.name;
  proxyConfig = serverConfig.proxy or { };
  proxySmokeTest = proxyConfig.smokeTest or { };
  proxyEnabled = proxyConfig.enable or (proxySmokeTest.enable or false);
in
{
  imports = [
    ../runtime/podman.nix
    ../services/postgres
    ../services/container-state.nix
    ../system/backups.nix
    ../networking/service-exposure.nix
    ../networking/proxy.nix
  ];

  my.proxy = lib.mkIf proxyEnabled {
    enable = true;
    acme = {
      email = proxyConfig.acmeEmail or "sander@epistola.app";
      dnsProvider = proxyConfig.acmeDnsProvider or "desec";
      environment = proxyConfig.acmeEnvironment or "staging";
      propagationDelayBeforeChecks = proxyConfig.acmePropagationDelayBeforeChecks or 0;
    };
    dns = {
      managedZones = proxyConfig.managedZones or [ "int.epistola.io" ];
      acmeDelegatedZone = proxyConfig.acmeDelegatedZone or "acme.epistola.io";
      publicTarget = proxyConfig.publicTarget or "eu1.netbird.services.";
      publicRecordType = proxyConfig.publicRecordType or "CNAME";
      netbirdCnameTarget = proxyConfig.netbirdCnameTarget or "${netbirdName}.epistola.int.";
    };
    testBackend = lib.mkIf (proxySmokeTest.enable or false) {
      enable = true;
      domain = proxySmokeTest.domain or "test.int.epistola.io";
      visibility = proxySmokeTest.visibility or "internal";
    };
  };
}
