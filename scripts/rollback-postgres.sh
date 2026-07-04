#!/usr/bin/env bash
# Rolls PostgreSQL data back to a named ZFS snapshot in the local prototype.
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 tank/data/postgres@snapshot" >&2
  exit 64
fi

snapshot="$1"
case "$snapshot" in
  tank/data/postgres@*) ;;
  *)
    echo "error: snapshot must belong to tank/data/postgres" >&2
    exit 64
    ;;
esac

systemctl stop apps.target
zfs rollback -r "$snapshot"
systemctl start apps.target

for _ in $(seq 1 60); do
  if pg_isready -h 127.0.0.1 -p 5432 -U demo -d demo -q; then
    echo "PostgreSQL is ready after rollback"
    exit 0
  fi
  sleep 1
done

echo "error: PostgreSQL did not become ready after rollback" >&2
exit 70
