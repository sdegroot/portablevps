# A portablevps consumer. Define your logical servers under servers/ and let
# portablevps assemble and operate them.
{
  description = "My portable single-instance servers";

  inputs = {
    # Pin portablevps from wherever you host it. During local development you
    # can point this at a checkout: url = "path:/path/to/portablevps";
    portablevps.url = "github:OWNER/portablevps?dir=portablevps";
  };

  outputs = { self, portablevps }:
    portablevps.lib.mkFlake {
      inherit self;
      serverDir = ./servers;
      # providerDir defaults to portablevps' built-in provider metadata; set it
      # to ./providers to add or override providers for this repository.
    };
}
