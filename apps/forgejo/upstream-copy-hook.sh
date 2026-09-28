#!/bin/sh
# pre-receive hook for an "upstream copy": a Forgejo repository whose refs belong
# to an upstream repository, except the private prefix, which is local work.
#
#   - the sync bot may move any ref outside the private prefix, and never one
#     inside it (so a sync can neither overwrite nor delete local work);
#   - everyone else may only create, move or delete branches under the prefix.
#
# Runs inside the Forgejo container (Forgejo 16 runs every executable in the
# repository's hooks/pre-receive.d), so: POSIX sh, no Nix store paths. The two
# values below are substituted by portablevps when the hook is installed.
bot="@BOT@"
prefix="@PREFIX@"

rc=0
while read -r _old _new ref; do
  case "$ref" in
    "refs/heads/$prefix"*)
      if [ "$GITEA_PUSHER_NAME" = "$bot" ]; then
        echo "refused: $ref is local work; the upstream sync never writes it" >&2
        rc=1
      fi
      ;;
    *)
      if [ "$GITEA_PUSHER_NAME" != "$bot" ]; then
        echo "refused: $ref is synced from upstream; push to refs/heads/$prefix* instead" >&2
        rc=1
      fi
      ;;
  esac
done
exit "$rc"
