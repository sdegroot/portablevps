# My portable servers

A [portablevps](https://github.com/sdegroot/portablevps) consumer repository.

## Layout

- `servers/<name>.nix` — one logical server per file.
- `secrets/<name>.yaml` — sops-encrypted secrets per server.
- `keys/` — committed public keys (private keys stay in `.local/`).
- `.sops.yaml` — age recipients allowed to decrypt secrets.

## First steps

1. Point the `portablevps` input in `flake.nix` at your published tool (or a
   local checkout during development).
2. Edit `servers/example.nix` (rename the file to your server name).
3. Generate the cloud admin keypair and per-server secrets, then fill in
   `.sops.yaml` recipients.
4. Provision and install with the portablevps CLI:

   ```sh
   nix run github:sdegroot/portablevps# -- preflight
   ```

See the portablevps README for the full provisioning, backup, restore, and
migration workflow.
