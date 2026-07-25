package core

// Tool flake references are variables so the Nix package can replace them with
// exact, lock-file-backed store paths through Go ldflags. Source builds retain
// convenient development fallbacks.
var (
	NixpkgsFlake       = "nixpkgs"
	NixosAnywhereFlake = "github:nix-community/nixos-anywhere"
)
