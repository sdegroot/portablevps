# portablevps apps: bundled, reusable application packages. Each app defines
# options under `portablevps.apps.<name>` and materialises its runtime only
# when enabled, so importing them all is cheap. Enabled per-server by the
# consumer (which supplies the deployment-specific config + assets).
{ ... }:
{
  imports = [
    ./authentik
    ./monitoring
  ];
}
