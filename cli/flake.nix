# The portablevps CLI: a self-contained Go project with its own flake, so it
# builds with a current Go toolchain and does not add a build-only input to the
# consumer-facing portablevps library flake (../flake.nix). Ready to become its
# own repository unchanged.
{
  description = "portablevps CLI — operate and migrate portable single-instance VPS servers";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    nixos-anywhere = {
      url = "github:nix-community/nixos-anywhere";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, nixos-anywhere }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in
    {
      packages = forAllSystems (system:
        let pkgs = nixpkgs.legacyPackages.${system}; in
        rec {
          portablevps = pkgs.buildGoModule {
            pname = "portablevps";
            version = "0.1.0";
            src = ./.;
            vendorHash = null; # dependencies are vendored in ./vendor
            subPackages = [ "cmd/portablevps" ];
            doCheck = true;
            checkPhase = ''
              runHook preCheck
              go test ./...
              runHook postCheck
            '';
            ldflags = [
              "-X github.com/epistola-app/portablevps/internal/core.NixpkgsFlake=${nixpkgs}"
              "-X github.com/epistola-app/portablevps/internal/core.NixosAnywhereFlake=${nixos-anywhere}"
            ];
            meta = {
              description = "Operate and migrate portable single-instance VPS servers";
              mainProgram = "portablevps";
            };
          };
          default = portablevps;
        });

      apps = forAllSystems (system: rec {
        portablevps = {
          type = "app";
          program = "${self.packages.${system}.portablevps}/bin/portablevps";
        };
        default = portablevps;
      });

      checks = forAllSystems (system: {
        cli = self.packages.${system}.portablevps;
      });

      devShells = forAllSystems (system:
        let pkgs = nixpkgs.legacyPackages.${system}; in
        {
          default = pkgs.mkShell {
            packages = [ pkgs.go pkgs.gopls pkgs.gotools ];
          };
        });
    };
}
