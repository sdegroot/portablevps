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
      # A tagged release build (GoReleaser) injects the real semver tag via
      # -X ...Version={{.Version}} instead; this is only what `nix build`/
      # `nix run` see outside of a release — a git revision beats a
      # permanently-stale hardcoded string.
      devVersion = "0.0.0-dev." + (self.shortRev or self.dirtyShortRev or "unknown");
    in
    {
      packages = forAllSystems (system:
        let pkgs = nixpkgs.legacyPackages.${system}; in
        rec {
          pvps = pkgs.buildGoModule {
            pname = "pvps";
            version = devVersion;
            src = ./.;
            vendorHash = null; # dependencies are vendored in ./vendor
            subPackages = [ "cmd/pvps" ];
            doCheck = true;
            checkPhase = ''
              runHook preCheck
              go test ./...
              runHook postCheck
            '';
            ldflags = [
              "-X github.com/sdegroot/portablevps/internal/core.NixpkgsFlake=${nixpkgs}"
              "-X github.com/sdegroot/portablevps/internal/core.NixosAnywhereFlake=${nixos-anywhere}"
              "-X github.com/sdegroot/portablevps/internal/core.Version=${devVersion}"
            ];
            meta = {
              description = "Operate and migrate portable single-instance VPS servers";
              mainProgram = "pvps";
            };
          };
          default = pvps;
        });

      apps = forAllSystems (system: rec {
        pvps = {
          type = "app";
          program = "${self.packages.${system}.pvps}/bin/pvps";
        };
        default = pvps;
      });

      checks = forAllSystems (system: {
        cli = self.packages.${system}.pvps;
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
