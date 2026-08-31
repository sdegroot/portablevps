# portablevps CLI

The operator and CI entrypoint for portablevps, written in Go. It runs against a
**consumer repository** (the one holding `servers/`, `secrets/`, `.local/`). It
now covers day-2 operations end to end (`server`, `service`, `secret`,
`network`, `backup`, `key`, `test`, `doctor`); the legacy Python CLI
(`../scripts/cloud.py`) remains alongside it for the handful of capabilities
not yet ported — see the repo root `README.md` and `docs/publishability-plan.md`
for the current split.

## Run it

```sh
# via Nix (no toolchain needed) — the CI-friendly path.
nix run github:sdegroot/portablevps -- doctor --server test-vps
# or from this directory (the CLI is its own flake):
nix run .#portablevps -- doctor --json          # machine-readable

# mise (see the repo root README's "Installing the CLI")
mise use -g github:sdegroot/portablevps

# from a checkout during development
go run ./cmd/portablevps doctor
```

The CLI operates on the consumer repo given by `--project` (default:
`$PORTABLEVPS_PROJECT`, else the current directory).

## Install a host safely

`server install` and `server adopt` send the server's age private key to the
target, so SSH host-key verification is mandatory by default. Obtain the rescue
environment's SSH host-key fingerprint from an independent provider channel,
create a dedicated OpenSSH `known_hosts` file, and pass it to the command:

```sh
portablevps server install my-server \
  --target root@203.0.113.10 \
  --host-key-file .local/known_hosts/my-server-rescue \
  --confirm root@203.0.113.10
```

The Nix package pins both nixpkgs and nixos-anywhere through its lock file, and
the installer preserves the consumer flake's committed transitive pins while
vendoring any `path:../<name>` inputs (so a remote/pure build can resolve them
without seeing the operator's local sibling checkout).

Only when a provider offers no independent way to verify the rescue host key,
the explicit `--insecure-skip-host-key-check` escape hatch restores the old
trust-on-first-use behavior. It can expose the age private key and should not be
used in production provisioning.

## Design

A serious CLI is not one big file. It is split into layers so no file is large,
the domain logic is unit-testable, and CI can drive everything headlessly:

```
cli/
  cmd/portablevps/        # main(): calls internal/cli
  internal/
    cli/                  # THIN command layer (cobra): parse flags, wire adapters, render.
                          #   No business logic. One small file per command.
    core/                 # Domain logic. Pure: no arg parsing, no process globals —
                          #   everything (command runner, env, repo root) is injected,
                          #   so it is trivially unit-testable and deterministic in CI.
    adapters/             # Side-effect boundaries (exec, PATH lookup). The only place
                          #   that touches the real system; tests mock here.
    config/               # Input precedence: flag > env > (future) portablevps.toml > default.
    output/               # Human and --json rendering. Every command supports --json.
```

Dependency direction is inward: `cli → core`, `cli → adapters`, and `core`
depends only on small interfaces it declares itself (e.g. `CommandRunner`), which
`adapters` implements. `core` never imports `cli` or `adapters`.

## Add a command

1. Put the logic in `internal/core/<name>.go` as a pure function taking a
   `core.Env` (or a narrower input struct) — no `os.Getenv`, no `exec` directly;
   use the injected `Runner` / `HasCommand`.
2. Add rendering to `internal/output` (human + JSON).
3. Add a thin `internal/cli/<name>.go` cobra command that resolves config, wires
   the real adapters, calls core, and renders. Register it in `root.go`.
4. Unit-test the core function with a fake runner and a temp repo dir (see
   `internal/core/doctor_test.go`). No test should shell out.

## CI conventions

- Every command supports `--json` for scriptable output.
- Exit codes are meaningful (`ExitError{Code, Message}`); a command returns one
  and the root maps it to the process exit code.
- Confirmation of destructive actions will be explicit flags (e.g.
  `--confirm-destroy <host>`), never a magic environment variable, so CI opts in
  deliberately.

## Test and build

```sh
cd cli
go test ./...            # unit tests
go vet ./...
go build ./...

# or through Nix (runs go test in the sandbox):
nix build .#checks.<system>.cli
```

Dependencies are vendored (`cli/vendor/`) so the Nix build is hermetic
(`vendorHash = null`). After changing dependencies, run `go mod tidy && go mod
vendor` and commit the result.
