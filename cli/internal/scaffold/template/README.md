# My portable servers

A [portablevps](https://github.com/sdegroot/portablevps) consumer repository.

## Layout

- `servers/<name>.nix` — one logical server per file.
- `secrets/<name>.yaml` — sops-encrypted secrets per server.
- `keys/` — committed public keys (private keys stay in `.local/`).
- `.sops.yaml` — age recipients allowed to decrypt secrets; `pvps secret init`
  maintains it.
- `portablevps.toml` — repository defaults for the CLI (`default_server`).
- `mise.toml` — the `pvps`, `sops` and `age` versions for this repository.

## First steps

1. `mise install` for the CLI and secret tooling (Nix is a separate install).
2. Edit `servers/{{.Server}}.nix`: provider placement, backup repository, your
   app image and the proxy domain. Copy it per additional server; the file name
   is the server name.
3. `git add` everything — Nix flakes only see tracked files.
4. Create the server's keys and secrets:

   ```sh
   pvps key generate {{.Server}}   # admin SSH keypair
   pvps secret init {{.Server}}    # age key + .sops.yaml rule
   pvps secret edit {{.Server}}    # fill in the declared secrets
   ```

5. `pvps doctor --server {{.Server}}` to check the repository, then provision
   and install with `pvps server --help`.

See the portablevps README for the full provisioning, backup, restore, and
migration workflow.
