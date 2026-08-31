package core

// Version is the CLI's build version, set via -ldflags at build time (the
// Nix build injects a git revision; a tagged release build injects the
// semver tag). "dev" when built without ldflags, e.g. a local `go build`.
var Version = "dev"
