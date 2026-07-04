#!/usr/bin/env bash
# Shared helpers for loading PostgreSQL/restic runtime env files and probing restic.

load_restic_env() {
  local env_restic_repository="${RESTIC_REPOSITORY:-}"
  local env_restic_password="${RESTIC_PASSWORD:-}"
  local env_aws_access_key_id="${AWS_ACCESS_KEY_ID:-}"
  local env_aws_secret_access_key="${AWS_SECRET_ACCESS_KEY:-}"
  local env_aws_default_region="${AWS_DEFAULT_REGION:-}"
  local env_aws_region="${AWS_REGION:-}"

  if [ -r /etc/epistola/restic.env ]; then
    # shellcheck disable=SC1091
    source /etc/epistola/restic.env
  fi

  RESTIC_REPOSITORY="${env_restic_repository:-${RESTIC_REPOSITORY:-/backup-repo}}"
  RESTIC_PASSWORD="${env_restic_password:-${RESTIC_PASSWORD:-dev-password}}"
  AWS_ACCESS_KEY_ID="${env_aws_access_key_id:-${AWS_ACCESS_KEY_ID:-}}"
  AWS_SECRET_ACCESS_KEY="${env_aws_secret_access_key:-${AWS_SECRET_ACCESS_KEY:-}}"
  AWS_DEFAULT_REGION="${env_aws_default_region:-${AWS_DEFAULT_REGION:-}}"
  AWS_REGION="${env_aws_region:-${AWS_REGION:-}}"

  export RESTIC_REPOSITORY RESTIC_PASSWORD
  export AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_DEFAULT_REGION AWS_REGION
}

load_postgres_env() {
  local env_pghost="${PGHOST:-}"
  local env_pgport="${PGPORT:-}"
  local env_pguser="${PGUSER:-}"
  local env_pgdatabase="${PGDATABASE:-}"
  local env_pgpassword="${PGPASSWORD:-}"

  PGHOST="${PGHOST:-127.0.0.1}"
  PGPORT="${PGPORT:-5432}"
  PGUSER="${PGUSER:-demo}"
  PGDATABASE="${PGDATABASE:-demo}"
  PGPASSWORD="${PGPASSWORD:-demo-password}"

  if [ -r /etc/epistola/postgres.env ]; then
    # shellcheck disable=SC1091
    source /etc/epistola/postgres.env
  fi

  PGHOST="${env_pghost:-$PGHOST}"
  PGPORT="${env_pgport:-$PGPORT}"
  PGUSER="${env_pguser:-$PGUSER}"
  PGDATABASE="${env_pgdatabase:-$PGDATABASE}"
  PGPASSWORD="${env_pgpassword:-$PGPASSWORD}"

  export PGHOST PGPORT PGUSER PGDATABASE PGPASSWORD
}

wait_restic_available() {
  local attempts="${1:-1}"
  local delay_seconds="${2:-0}"

  for _ in $(seq 1 "$attempts"); do
    if restic snapshots >/dev/null 2>&1; then
      return 0
    fi
    sleep "$delay_seconds"
  done

  return 1
}
