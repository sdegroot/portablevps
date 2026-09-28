# Provision per-organization package-publishing bots for Forgejo Actions.
#
# Forgejo's automatic job token cannot publish packages, so each enabled org gets
# a local bot that may only publish packages there: provision-orgs has already
# created the org's "package-publishers" team (repo.packages = write); this puts
# the bot in it, keeps a write:package token for it on the host, and publishes
# that token as an org Actions secret and the bot's name as an org Actions
# variable. Secret and variable are (re)written on every run, so a deleted one
# comes back; the token is only re-issued when it is missing or no longer valid
# (e.g. after a restore onto a new host - it is deliberately not backed up).
#
# Inputs (environment):
#   FORGEJO_API                  e.g. http://127.0.0.1:3000/api/v1
#   FORGEJO_ADMIN_USER           break-glass admin username
#   FORGEJO_ADMIN_PASSWORD_FILE  file holding its password
#   FORGEJO_CLI                  command running the forgejo CLI in the container
#   PUBLISHERS_JSON              { org: { username, secretName, userVariable } }
#   PUBLISHER_STATE_DIR          where the bots' tokens are kept (0700)
set -euo pipefail

: "${FORGEJO_API:?}" "${FORGEJO_ADMIN_USER:?}" "${FORGEJO_ADMIN_PASSWORD_FILE:?}" "${FORGEJO_CLI:?}"
: "${PUBLISHERS_JSON:?}" "${PUBLISHER_STATE_DIR:?}"

curl_cfg="$(mktemp)"
trap 'rm -f "$curl_cfg"' EXIT
chmod 600 "$curl_cfg"
escape() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }
printf 'user = "%s:%s"\n' "$(escape "$FORGEJO_ADMIN_USER")" "$(escape "$(tr -d '\n' < "$FORGEJO_ADMIN_PASSWORD_FILE")")" > "$curl_cfg"

api() { curl -sS --fail-with-body -K "$curl_cfg" -H 'Content-Type: application/json' "$@"; }
status() { curl -sS -o /dev/null -w '%{http_code}' -K "$curl_cfg" -H 'Content-Type: application/json' "$@"; }

install -d -m 700 "$PUBLISHER_STATE_DIR"

jq -r 'keys[]' "$PUBLISHERS_JSON" | while IFS= read -r org; do
  bot="$(jq -r --arg o "$org" '.[$o].username' "$PUBLISHERS_JSON")"
  secret="$(jq -r --arg o "$org" '.[$o].secretName' "$PUBLISHERS_JSON")"
  variable="$(jq -r --arg o "$org" '.[$o].userVariable' "$PUBLISHERS_JSON")"
  token_file="$PUBLISHER_STATE_DIR/$org.token"

  # --- the bot ---------------------------------------------------------------
  code="$(status "$FORGEJO_API/users/$bot")"
  case "$code" in
    200) ;;
    404)
      echo "forgejo-provision: creating package publisher $bot for $org"
      api -X POST "$FORGEJO_API/admin/users" -d "$(jq -nc --arg u "$bot" --arg p "$(head -c 32 /dev/urandom | base64)" \
        '{username: $u, email: ($u + "@noreply.invalid"), password: $p, must_change_password: false, visibility: "private"}')" >/dev/null
      ;;
    *) echo "forgejo-provision: GET /users/$bot returned HTTP $code" >&2; exit 1 ;;
  esac

  # --- its team (created by provision-orgs) ------------------------------------
  team_id="$(api "$FORGEJO_API/orgs/$org/teams?limit=50" | jq -r '.[] | select(.name == "package-publishers") | .id')"
  [ -n "$team_id" ] || { echo "forgejo-provision: team $org/package-publishers not found" >&2; exit 1; }
  api -X PUT "$FORGEJO_API/teams/$team_id/members/$bot" >/dev/null

  # --- its token: valid if it can read the org's packages -----------------------
  valid=no
  if [ -s "$token_file" ]; then
    code="$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: token $(tr -d '\n' < "$token_file")" "$FORGEJO_API/packages/$org")"
    [ "$code" = 200 ] && valid=yes
  fi
  if [ "$valid" = no ]; then
    echo "forgejo-provision: issuing a package token for $bot"
    ( umask 077
      # shellcheck disable=SC2086 # FORGEJO_CLI is a command line by design
      $FORGEJO_CLI admin user generate-access-token --username "$bot" \
        --token-name "package-publisher-$(date +%s)" --scopes write:package --raw \
        | tr -d '\r\n' > "$token_file" )
    [ -s "$token_file" ] || { echo "forgejo-provision: token generation for $bot failed" >&2; exit 1; }
  fi

  # --- publish it to the org's Actions (every run: self-healing) ---------------
  jq -n --rawfile t "$token_file" '{data: ($t | rtrimstr("\n"))}' \
    | api -X PUT "$FORGEJO_API/orgs/$org/actions/secrets/$secret" -d @- >/dev/null
  body="$(jq -nc --arg n "$variable" --arg v "$bot" '{name: $n, value: $v}')"
  code="$(status -X PUT "$FORGEJO_API/orgs/$org/actions/variables/$variable" -d "$body")"
  case "$code" in
    2??) ;;
    404) api -X POST "$FORGEJO_API/orgs/$org/actions/variables/$variable" -d "$body" >/dev/null ;;
    *) echo "forgejo-provision: setting variable $org/$variable returned HTTP $code" >&2; exit 1 ;;
  esac
done
