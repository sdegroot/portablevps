#!/usr/bin/env bash
# Prints restic/S3 environment exports for QEMU guests to reach host-local MinIO.
set -euo pipefail

bucket="${MINIO_BUCKET:-epistola-dr}"
api_port="${MINIO_API_PORT:-9000}"
access_key="${MINIO_ROOT_USER:-epistola}"
secret_key="${MINIO_ROOT_PASSWORD:-epistola-minio-password}"
restic_password="${RESTIC_PASSWORD:-dev-password}"
guest_host="${MINIO_GUEST_HOST:-10.0.2.2}"

cat <<EOF
export RESTIC_REPOSITORY='s3:http://$guest_host:$api_port/$bucket'
export RESTIC_PASSWORD='$restic_password'
export AWS_ACCESS_KEY_ID='$access_key'
export AWS_SECRET_ACCESS_KEY='$secret_key'
EOF
