#!/usr/bin/env bash
# Starts a host-local MinIO container for self-contained S3-compatible testing.
set -euo pipefail

runtime="${CONTAINER_RUNTIME:-}"
if [ -z "$runtime" ]; then
  if command -v podman >/dev/null 2>&1; then
    runtime="podman"
  elif command -v docker >/dev/null 2>&1; then
    runtime="docker"
  else
    echo "error: podman or docker is required" >&2
    exit 69
  fi
fi

container_name="${MINIO_CONTAINER_NAME:-epistola-minio}"
network_name="${MINIO_NETWORK_NAME:-epistola-minio}"
data_dir="${MINIO_DATA_DIR:-.local/minio/data}"
address="${MINIO_ADDRESS:-127.0.0.1}"
api_port="${MINIO_API_PORT:-9000}"
console_port="${MINIO_CONSOLE_PORT:-9001}"
bucket="${MINIO_BUCKET:-epistola-dr}"
access_key="${MINIO_ROOT_USER:-epistola}"
secret_key="${MINIO_ROOT_PASSWORD:-epistola-minio-password}"
minio_image="${MINIO_IMAGE:-quay.io/minio/minio:latest}"
mc_image="${MINIO_MC_IMAGE:-quay.io/minio/mc:latest}"

sync_podman_machine_clock() {
  if [ "$runtime" != "podman" ]; then
    return 0
  fi

  if ! "$runtime" machine ssh --help >/dev/null 2>&1; then
    return 0
  fi

  host_epoch="$(date -u +%s)"
  "$runtime" machine ssh "sudo date -u -s '@$host_epoch' >/dev/null" >/dev/null 2>&1 || true
}

sync_podman_machine_clock

mkdir -p "$data_dir"

if ! "$runtime" network inspect "$network_name" >/dev/null 2>&1; then
  "$runtime" network create "$network_name" >/dev/null
fi

if "$runtime" container inspect "$container_name" >/dev/null 2>&1; then
  if [ "$("$runtime" container inspect -f '{{.State.Running}}' "$container_name")" != "true" ]; then
    "$runtime" start "$container_name" >/dev/null
  fi
else
  "$runtime" run \
    --detach \
    --name "$container_name" \
    --network "$network_name" \
    --publish "$address:$api_port:9000" \
    --publish "$address:$console_port:9001" \
    --env "MINIO_ROOT_USER=$access_key" \
    --env "MINIO_ROOT_PASSWORD=$secret_key" \
    --volume "$PWD/$data_dir:/data" \
    "$minio_image" \
    server /data --address :9000 --console-address :9001 >/dev/null
fi

for _ in $(seq 1 60); do
  if curl --fail --silent --output /dev/null "http://$address:$api_port/minio/health/ready"; then
    break
  fi
  sleep 1
done

if ! curl --fail --silent --output /dev/null "http://$address:$api_port/minio/health/ready"; then
  echo "error: MinIO did not become ready at http://$address:$api_port" >&2
  exit 70
fi

"$runtime" run --rm --network "$network_name" --entrypoint /bin/sh "$mc_image" \
  -c "mc alias set local http://$container_name:9000 '$access_key' '$secret_key' >/dev/null && mc mb --ignore-existing local/'$bucket'" >/dev/null

cat <<EOF
MinIO is ready:
  API:     http://$address:$api_port
  Console: http://$address:$console_port
  Bucket:  $bucket

For QEMU VM tests:
  eval "\$(scripts/local-s3-env.sh)"
EOF
