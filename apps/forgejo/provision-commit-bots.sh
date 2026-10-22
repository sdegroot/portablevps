# Provision Forgejo commit bots: local accounts that commit from automation.
#
# Each bot gets write access to its repositories, a write:repository token kept
# on the host, and that token as an Actions secret on each repository. The
# secret is rewritten on every run, so a deleted one comes back; the token is
# only re-issued when it is missing or no longer valid (e.g. after a restore
# onto a new host - it is deliberately not backed up).
#
# With SIGNING_ELIGIBLE=true a bot without any key also gets an SSH public key
# whose private half is thrown away here. Forgejo's "pubkey" signing rule only
# asks whether the account has a key, so this makes the commits the bot creates
# through the API signed by Forgejo. Nobody can log in or sign with that key.
#
# Inputs (environment):
#   FORGEJO_API                  e.g. http://127.0.0.1:3000/api/v1
#   FORGEJO_ADMIN_USER           break-glass admin username
#   FORGEJO_ADMIN_PASSWORD_FILE  file holding its password
#   FORGEJO_CLI                  command running the forgejo CLI in the container
#   BOTS_JSON                    { bot: { repositories: ["owner/repo"], secretName } }
#   BOT_STATE_DIR                where the bots' tokens are kept (0700)
#   SIGNING_ELIGIBLE             true: give each bot a key (see above)
set -euo pipefail

: "${FORGEJO_API:?}" "${FORGEJO_ADMIN_USER:?}" "${FORGEJO_ADMIN_PASSWORD_FILE:?}" "${FORGEJO_CLI:?}"
: "${BOTS_JSON:?}" "${BOT_STATE_DIR:?}" "${SIGNING_ELIGIBLE:?}"

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
curl_cfg="$workdir/curl.cfg"
( umask 077; : > "$curl_cfg" )
escape() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }
printf 'user = "%s:%s"\n' "$(escape "$FORGEJO_ADMIN_USER")" "$(escape "$(tr -d '\n' < "$FORGEJO_ADMIN_PASSWORD_FILE")")" > "$curl_cfg"

api() { curl -sS --fail-with-body -K "$curl_cfg" -H 'Content-Type: application/json' "$@"; }
status() { curl -sS -o /dev/null -w '%{http_code}' -K "$curl_cfg" -H 'Content-Type: application/json' "$@"; }

install -d -m 700 "$BOT_STATE_DIR"

jq -r 'keys[]' "$BOTS_JSON" | while IFS= read -r bot; do
  secret="$(jq -r --arg b "$bot" '.[$b].secretName' "$BOTS_JSON")"
  token_file="$BOT_STATE_DIR/$bot.token"
  first_repo="$(jq -r --arg b "$bot" '.[$b].repositories[0]' "$BOTS_JSON")"

  # --- the bot: a local account with no password anyone knows -------------------
  code="$(status "$FORGEJO_API/users/$bot")"
  case "$code" in
    200) ;;
    404)
      echo "forgejo-provision: creating commit bot $bot"
      api -X POST "$FORGEJO_API/admin/users" -d "$(jq -nc --arg u "$bot" --arg p "$(head -c 32 /dev/urandom | base64)" \
        '{username: $u, email: ($u + "@noreply.invalid"), password: $p, must_change_password: false, visibility: "private"}')" >/dev/null
      ;;
    *) echo "forgejo-provision: GET /users/$bot returned HTTP $code" >&2; exit 1 ;;
  esac

  # --- write access, before the token check below needs read access ------------
  jq -r --arg b "$bot" '.[$b].repositories[]' "$BOTS_JSON" | while IFS= read -r repo; do
    code="$(status -X PUT "$FORGEJO_API/repos/$repo/collaborators/$bot" -d '{"permission":"write"}')"
    case "$code" in
      2??) ;;
      404) echo "forgejo-provision: repository $repo for commit bot $bot does not exist" >&2; exit 1 ;;
      *) echo "forgejo-provision: adding $bot to $repo returned HTTP $code" >&2; exit 1 ;;
    esac
  done

  # --- its token: valid if it can read its first repository -------------------
  valid=no
  if [ -s "$token_file" ]; then
    code="$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: token $(tr -d '\n' < "$token_file")" "$FORGEJO_API/repos/$first_repo")"
    [ "$code" = 200 ] && valid=yes
  fi
  if [ "$valid" = no ]; then
    echo "forgejo-provision: issuing a repository token for $bot"
    ( umask 077
      # shellcheck disable=SC2086 # FORGEJO_CLI is a command line by design
      $FORGEJO_CLI admin user generate-access-token --username "$bot" \
        --token-name "commit-bot-$(date +%s)" --scopes write:repository --raw \
        | tr -d '\r\n' > "$token_file" )
    [ -s "$token_file" ] || { echo "forgejo-provision: token generation for $bot failed" >&2; exit 1; }
  fi

  # --- publish it to each repository's Actions (every run: self-healing) --------
  jq -r --arg b "$bot" '.[$b].repositories[]' "$BOTS_JSON" | while IFS= read -r repo; do
    jq -n --rawfile t "$token_file" '{data: ($t | rtrimstr("\n"))}' \
      | api -X PUT "$FORGEJO_API/repos/$repo/actions/secrets/$secret" -d @- >/dev/null
  done

  # --- a key, so Forgejo signs what the bot commits through the API -------------
  if [ "$SIGNING_ELIGIBLE" = true ] && [ "$(api "$FORGEJO_API/users/$bot/keys" | jq length)" = 0 ]; then
    echo "forgejo-provision: giving $bot a key for Forgejo's pubkey signing rule"
    ssh-keygen -q -t ed25519 -N '' -C "$bot (signing eligibility only; private key discarded)" -f "$workdir/$bot"
    jq -n --arg k "$(cat "$workdir/$bot.pub")" '{title: "signing eligibility (private key discarded)", key: $k}' \
      | api -X POST "$FORGEJO_API/admin/users/$bot/keys" -d @- >/dev/null
    rm -f "$workdir/$bot" "$workdir/$bot.pub"
  fi
done
