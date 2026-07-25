# portablevps: portable single-instance VPS installations on NixOS.
#
# Exports a flake library (lib.mkFlake) and reusable NixOS modules that
# consumer repositories use to define their own servers, plus the tool's own
# local QEMU hosts used to prove the backup/restore flow.
{
  description = "portablevps — portable single-instance VPS installations on NixOS";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";
    nixpkgs-postgres.url = "github:NixOS/nixpkgs/nixos-unstable";
    nixpkgs-netbird.url = "github:NixOS/nixpkgs/nixos-unstable";
    disko.url = "github:nix-community/disko";
    disko.inputs.nixpkgs.follows = "nixpkgs";
    sops-nix.url = "github:Mic92/sops-nix";
    sops-nix.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs = { self, nixpkgs, nixpkgs-postgres, nixpkgs-netbird, disko, sops-nix }:
    let
      pvlib = import ./lib {
        inherit nixpkgs nixpkgs-postgres nixpkgs-netbird disko sops-nix;
        toolRoot = ./.;
      };
    in
    {
      lib = pvlib;

      templates = {
        server = {
          path = ./templates/server;
          description = "A portablevps consumer repository defining one or more servers";
        };
        default = self.templates.server;
      };

      # Reusable modules for consumers who compose hosts by hand instead of
      # through mkFlake.
      nixosModules = {
        base = ./modules/system/base.nix;
        backups = ./modules/system/backups.nix;
        telemetry = ./modules/system/telemetry.nix;
        restoreMode = ./modules/system/restore-mode.nix;
        secrets = ./modules/system/secrets.nix;
        deployment = ./modules/system/deployment.nix;
        scripts = ./modules/system/scripts.nix;
        postgres = ./modules/services/postgres;
        containerState = ./modules/services/container-state.nix;
        podman = ./modules/runtime/podman.nix;
        docker = ./modules/runtime/docker.nix;
        network = ./modules/networking/network.nix;
        netbird = ./modules/networking/netbird.nix;
        tailscale = ./modules/networking/tailscale.nix;
        proxy = ./modules/networking/proxy.nix;
        serviceExposure = ./modules/networking/service-exposure.nix;
        breakGlassSsh = ./modules/networking/break-glass-ssh.nix;
        cloudVps = ./modules/platforms/cloud-vps.nix;
        forgejoRunner = ./modules/profiles/forgejo-runner.nix;
        discourseApp = ./modules/profiles/discourse-app.nix;
        singleInstanceApp = ./modules/profiles/single-instance-app.nix;
      };

      # Hermetic NixOS VM tests. These boot Linux VMs, so they are exposed only
      # for Linux systems and run in CI; `nix flake check` on macOS skips them.
      # (The CLI has its own flake at ./cli with its own checks.)
      checks = nixpkgs.lib.genAttrs [ "x86_64-linux" "aarch64-linux" ] (system: {
        restore-mode = import ./tests/vm/restore-mode.nix {
          pkgs = nixpkgs.legacyPackages.${system};
        };
      });

      # The tool's own disaster-recovery test hosts (local QEMU, aarch64).
      nixosConfigurations = {
        local-vm = pvlib.mkHost {
          inherit self;
          system = "aarch64-linux";
          hostModule = ./hosts/local-vm.nix;
        };

        local-vm-restore = pvlib.mkHost {
          inherit self;
          system = "aarch64-linux";
          hostModule = ./hosts/local-vm.nix;
          restoreMode = true;
        };

        discourse-local-vm = pvlib.mkHost {
          inherit self;
          system = "aarch64-linux";
          hostModule = ./hosts/discourse-local-vm.nix;
        };

        discourse-local-vm-restore = pvlib.mkHost {
          inherit self;
          system = "aarch64-linux";
          hostModule = ./hosts/discourse-local-vm.nix;
          restoreMode = true;
        };
      };
    };
}
