# Disaster Recovery Test

`test-disaster-recovery.sh` is a host-orchestrated test for the local VM
prototype. It intentionally uses two VMs so the backup is restored onto a fresh
server instead of relying on VM snapshots.

Run from the macOS host after cloning this repository into both VMs:

```sh
VM_A_SSH=admin@vm-a.local \
VM_B_SSH=admin@vm-b.local \
REMOTE_REPO=/home/admin/portablevps-nix-infra \
tests/test-disaster-recovery.sh
```

For the included QEMU helpers, boot VM A with host port `2222` and VM B with
host port `2223`, then run:

```sh
VM_A_SSH="-p 2222 admin@localhost" \
VM_B_SSH="-p 2223 admin@localhost" \
REMOTE_REPO=/home/admin/portablevps-nix-infra \
tests/test-disaster-recovery.sh
```

For the repository's default QEMU VM names and ports, use the Taskfile
validation entrypoint instead:

```sh
mise exec -- task validate:qemu
```

It waits for both VMs, mounts the host repository at `/host`, resets local
MinIO, and runs the full disaster recovery test.

The test fails if:

- `postgres.service` starts unexpectedly while VM B is in restore mode.
- the PostgreSQL marker inserted on VM A is missing on VM B.

Manual VM lifecycle is acceptable for the first prototype. Create VM B from a
fresh install or destroy/recreate VM A as VM B before running the restore half.
