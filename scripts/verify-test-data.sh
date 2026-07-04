#!/usr/bin/env bash
# Verifies that a PostgreSQL recovery marker exists after restore.
set -euo pipefail

mode="present"
if [ "${1:-}" = "--absent" ]; then
  mode="absent"
  shift
fi

marker="${1:-}"
if [ -z "$marker" ]; then
  if [ ! -r /data/recovery-marker.txt ]; then
    echo "error: marker argument missing and /data/recovery-marker.txt is unavailable" >&2
    exit 64
  fi
  marker="$(tr -d '\n' </data/recovery-marker.txt)"
fi

PGHOST="${PGHOST:-127.0.0.1}"
PGPORT="${PGPORT:-5432}"
PGUSER="${PGUSER:-demo}"
PGDATABASE="${PGDATABASE:-demo}"
PGPASSWORD="${PGPASSWORD:-demo-password}"
if [ -r /etc/epistola/postgres.env ]; then
  # shellcheck disable=SC1091
  source /etc/epistola/postgres.env
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

count="$(
  psql -v ON_ERROR_STOP=1 -v marker="$marker" -At <<'SQL'
SELECT count(*) FROM recovery_test WHERE marker = :'marker';
SQL
)"

case "$mode:$count" in
  present:0)
    echo "error: marker missing: $marker" >&2
    exit 1
    ;;
  absent:0)
    echo "marker absent as expected: $marker"
    ;;
  absent:*)
    echo "error: marker unexpectedly present: $marker" >&2
    exit 1
    ;;
  *)
    echo "marker verified: $marker"
    ;;
esac
