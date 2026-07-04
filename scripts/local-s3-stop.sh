#!/usr/bin/env bash
# Stops host-local MinIO and optionally removes its object data.
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
data_dir="${MINIO_DATA_DIR:-.local/minio/data}"
remove_data=false

if [ "${1:-}" = "--remove-data" ]; then
  remove_data=true
elif [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  cat >&2 <<'USAGE'
usage: scripts/local-s3-stop.sh [--remove-data]

Stops the local MinIO container. With --remove-data, also removes
.local/minio/data so the next test starts with an empty object store.
USAGE
  exit 0
elif [ "$#" -gt 0 ]; then
  echo "error: unknown argument: $1" >&2
  exit 64
fi

if "$runtime" container inspect "$container_name" >/dev/null 2>&1; then
  "$runtime" rm -f "$container_name" >/dev/null
fi

if [ "$remove_data" = "true" ]; then
  rm -rf "$data_dir"
fi
