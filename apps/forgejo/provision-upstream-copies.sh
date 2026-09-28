# Provision Forgejo "upstream copies": the sync bot, its token, the repositories
# (created on first run), the bot's write access, and the pre-receive hook that
# keeps everything outside the private prefix upstream-owned.
#
# Inputs (environment):
#   FORGEJO_API                  e.g. http://127.0.0.1:3000/api/v1
#   FORGEJO_ADMIN_USER           break-glass admin username
#   FORGEJO_ADMIN_PASSWORD_FILE  file holding its password
#   FORGEJO_CLI                  command running the forgejo CLI in the container
#   UPSTREAM_BOT                 the sync bot's username
#   UPSTREAM_TOKEN_FILE          where the bot's API token is kept (0600)
#   UPSTREAM_COPIES_JSON         { "owner/repo": { upstream, privatePrefix, hook } }
#   FORGEJO_REPOS_ROOT           host path of Forgejo's git/repositories
#   FORGEJO_UID                  uid owning Forgejo's data
set -euo pipefail

: "${FORGEJO_API:?}" "${FORGEJO_ADMIN_USER:?}" "${FORGEJO_ADMIN_PASSWORD_FILE:?}" "${FORGEJO_CLI:?}"
: "${UPSTREAM_BOT:?}" "${UPSTREAM_TOKEN_FILE:?}" "${UPSTREAM_COPIES_JSON:?}" "${FORGEJO_REPOS_ROOT:?}" "${FORGEJO_UID:?}"

curl_cfg="$(mktemp)"
trap 'rm -f "$curl_cfg"' EXIT
chmod 600 "$curl_cfg"
escape() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }
printf 'user = "%s:%s"\n' "$(escape "$FORGEJO_ADMIN_USER")" "$(escape "$(tr -d '\n' < "$FORGEJO_ADMIN_PASSWORD_FILE")")" > "$curl_cfg"

api() { curl -sS --fail-with-body -K "$curl_cfg" -H 'Content-Type: application/json' "$@"; }
status() { curl -sS -o /dev/null -w '%{http_code}' -K "$curl_cfg" "$@"; }

# --- the bot: a local account with no password anyone knows ---------------------
code="$(status "$FORGEJO_API/users/$UPSTREAM_BOT")"
case "$code" in
  200) ;;
  404)
    echo "forgejo-provision: creating upstream-sync bot $UPSTREAM_BOT"
    api -X POST "$FORGEJO_API/admin/users" -d "$(jq -nc --arg u "$UPSTREAM_BOT" --arg p "$(head -c 32 /dev/urandom | base64)" \
      '{username: $u, email: ($u + "@noreply.invalid"), password: $p, must_change_password: false, visibility: "private"}')" >/dev/null
    ;;
  *)
    echo "forgejo-provision: GET /users/$UPSTREAM_BOT returned HTTP $code" >&2
    exit 1
    ;;
esac

# --- its token: kept on the host, regenerated if missing or no longer valid ------
token_ok=no
if [ -s "$UPSTREAM_TOKEN_FILE" ]; then
  code="$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: token $(tr -d '\n' < "$UPSTREAM_TOKEN_FILE")" "$FORGEJO_API/user")"
  [ "$code" = 200 ] && token_ok=yes
fi
if [ "$token_ok" = no ]; then
  echo "forgejo-provision: issuing an API token for $UPSTREAM_BOT"
  install -d -m 700 "$(dirname "$UPSTREAM_TOKEN_FILE")"
  umask 077
  # shellcheck disable=SC2086 # FORGEJO_CLI is a command line by design
  $FORGEJO_CLI admin user generate-access-token --username "$UPSTREAM_BOT" \
    --token-name "upstream-sync-$(date +%s)" --scopes write:repository --raw \
    | tr -d '\r\n' > "$UPSTREAM_TOKEN_FILE"
  [ -s "$UPSTREAM_TOKEN_FILE" ] || { echo "forgejo-provision: token generation failed" >&2; exit 1; }
fi

# --- each copy: repository, bot access, hook --------------------------------------
jq -r 'keys[]' "$UPSTREAM_COPIES_JSON" | while IFS= read -r full; do
  owner="${full%%/*}"; repo="${full#*/}"
  upstream="$(jq -r --arg k "$full" '.[$k].upstream' "$UPSTREAM_COPIES_JSON")"
  hook="$(jq -r --arg k "$full" '.[$k].hook' "$UPSTREAM_COPIES_JSON")"

  code="$(status "$FORGEJO_API/repos/$owner/$repo")"
  if [ "$code" = 404 ]; then
    default_branch="$(git ls-remote --symref "$upstream" HEAD | sed -n 's#^ref: refs/heads/\(.*\)\tHEAD$#\1#p')"
    body="$(jq -nc --arg n "$repo" --arg b "${default_branch:-main}" --arg u "$upstream" \
      '{name: $n, private: true, auto_init: false, default_branch: $b,
        description: ("Copy of " + $u + " (synced; push only to the private prefix)")}')"
    echo "forgejo-provision: creating upstream copy $full (default branch ${default_branch:-main})"
    if [ "$(status "$FORGEJO_API/orgs/$owner")" = 200 ]; then
      api -X POST "$FORGEJO_API/orgs/$owner/repos" -d "$body" >/dev/null
    else
      api -X POST "$FORGEJO_API/admin/users/$owner/repos" -d "$body" >/dev/null
    fi
  elif [ "$code" = 200 ]; then
    if [ "$(api "$FORGEJO_API/repos/$owner/$repo" | jq -r .mirror)" = true ]; then
      echo "forgejo-provision: $full is a pull mirror, which cannot take pushes; delete or convert it first" >&2
      exit 1
    fi
  else
    echo "forgejo-provision: GET /repos/$full returned HTTP $code" >&2
    exit 1
  fi

  api -X PUT "$FORGEJO_API/repos/$owner/$repo/collaborators/$UPSTREAM_BOT" -d '{"permission":"write"}' >/dev/null

  # Forgejo lower-cases owner and repository directory names on disk.
  dir="$FORGEJO_REPOS_ROOT/$(printf '%s' "$owner" | tr '[:upper:]' '[:lower:]')/$(printf '%s' "$repo" | tr '[:upper:]' '[:lower:]').git"
  [ -d "$dir" ] || { echo "forgejo-provision: repository directory $dir not found" >&2; exit 1; }
  install -d -m 755 "$dir/hooks" "$dir/hooks/pre-receive.d"
  install -m 755 "$hook" "$dir/hooks/pre-receive.d/upstream-copy"
  # Owned like the rest of Forgejo's data; only root can hand files over (the
  # provision unit runs as root; a non-root test run leaves them as its own).
  if [ "$(id -u)" = 0 ]; then
    chown "$FORGEJO_UID:$FORGEJO_UID" "$dir/hooks" "$dir/hooks/pre-receive.d" "$dir/hooks/pre-receive.d/upstream-copy"
  fi
done
