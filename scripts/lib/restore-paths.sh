#!/usr/bin/env bash
# Shared destructive-restore path validation.

canonical_restore_clear_path() {
  local path="${1:-}" current segment
  local -a segments

  case "$path" in
    /data/* | /var/lib/*) ;;
    *)
      echo "error: refusing restore clear path outside /data or /var/lib: $path" >&2
      return 78
      ;;
  esac

  # Reject traversal and duplicate/trailing separators without relying on GNU
  # realpath extensions, so the guard is testable on both Linux and macOS.
  case "$path" in
    *//* | */./* | */../* | */. | */.. | */)
      echo "error: refusing non-canonical restore clear path: $path" >&2
      return 78
      ;;
  esac

  # Refuse any existing symlink in the path. Otherwise a declarative
  # /data/app/state could resolve through /data/app -> /etc and escape the
  # allow-list even though the string itself is clean.
  IFS='/' read -r -a segments <<<"$path"
  current=""
  for segment in "${segments[@]}"; do
    [ -n "$segment" ] || continue
    current="$current/$segment"
    if [ -L "$current" ]; then
      echo "error: refusing symlinked restore clear path: $path ($current is a symlink)" >&2
      return 78
    fi
  done

  printf '%s\n' "$path"
}
