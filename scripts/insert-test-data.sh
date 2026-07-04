#!/usr/bin/env bash
# Inserts a PostgreSQL recovery marker used by backup and restore validation.
set -euo pipefail

marker="${1:-recovery-$(date -u +%Y%m%dT%H%M%SZ)-$RANDOM}"

PGHOST="${PGHOST:-127.0.0.1}"
PGPORT="${PGPORT:-5432}"
PGUSER="${PGUSER:-demo}"
PGDATABASE="${PGDATABASE:-demo}"
PGPASSWORD="${PGPASSWORD:-demo-password}"
if [ -r /etc/portablevps/postgres.env ]; then
  # shellcheck disable=SC1091
  source /etc/portablevps/postgres.env
fi
export PGHOST PGPORT PGUSER PGDATABASE PGPASSWORD

for _ in $(seq 1 60); do
  if pg_isready -q; then
    break
  fi
  sleep 1
done

if ! pg_isready -q; then
  echo "error: PostgreSQL is not ready" >&2
  exit 70
fi

psql -v ON_ERROR_STOP=1 -v marker="$marker" <<'SQL'
CREATE TABLE IF NOT EXISTS recovery_test (
  id serial primary key,
  marker text not null,
  created_at timestamptz default now()
);
INSERT INTO recovery_test (marker) VALUES (:'marker');
SQL

mkdir -p /data
printf '%s\n' "$marker" >/data/recovery-marker.txt
echo "$marker"
