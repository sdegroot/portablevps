# Upsert Forgejo organizations and teams from a JSON document, via the API,
# as the break-glass admin. Additive: anything not in the document is left
# alone (no deletes), so a manual org/team is never destroyed by a deploy.
#
# Inputs (environment):
#   FORGEJO_API          e.g. http://127.0.0.1:3000/api/v1
#   FORGEJO_ADMIN_USER   break-glass admin username
#   FORGEJO_ADMIN_PASSWORD_FILE  file holding its password
#   FORGEJO_ORGS_JSON    { org: { full_name, description, visibility,
#                          teams: { team: { description, units, units_map,
#                          includes_all_repositories, can_create_org_repo } } } }
set -euo pipefail

: "${FORGEJO_API:?}" "${FORGEJO_ADMIN_USER:?}" "${FORGEJO_ADMIN_PASSWORD_FILE:?}" "${FORGEJO_ORGS_JSON:?}"

# Credentials go through a curl config file, never argv (visible in ps).
curl_cfg="$(mktemp)"
trap 'rm -f "$curl_cfg"' EXIT
chmod 600 "$curl_cfg"
password="$(tr -d '\n' < "$FORGEJO_ADMIN_PASSWORD_FILE")"
escape() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }
printf 'user = "%s:%s"\n' "$(escape "$FORGEJO_ADMIN_USER")" "$(escape "$password")" > "$curl_cfg"

api() {
  curl -sS --fail-with-body -K "$curl_cfg" -H 'Content-Type: application/json' "$@"
}

status() {
  curl -sS -o /dev/null -w '%{http_code}' -K "$curl_cfg" "$1"
}

jq -r 'keys[]' "$FORGEJO_ORGS_JSON" | while IFS= read -r org; do
  org_body="$(jq -c --arg o "$org" '.[$o] | {full_name, description, visibility}' "$FORGEJO_ORGS_JSON")"
  code="$(status "$FORGEJO_API/orgs/$org")"
  case "$code" in
    404)
      echo "forgejo-provision: creating org $org"
      api -X POST "$FORGEJO_API/orgs" -d "$(jq -c --arg o "$org" '. + {username: $o}' <<<"$org_body")" >/dev/null
      ;;
    200)
      api -X PATCH "$FORGEJO_API/orgs/$org" -d "$org_body" >/dev/null
      ;;
    *)
      echo "forgejo-provision: GET /orgs/$org returned HTTP $code" >&2
      exit 1
      ;;
  esac

  # limit=50 is the API maximum per page; an org with more teams than that is
  # far outside what this is for, so fail loudly rather than paginate.
  existing="$(api "$FORGEJO_API/orgs/$org/teams?limit=50")"
  if [ "$(jq length <<<"$existing")" -ge 50 ]; then
    echo "forgejo-provision: org $org has >= 50 teams; refusing to guess" >&2
    exit 1
  fi

  jq -r --arg o "$org" '.[$o].teams | keys[]' "$FORGEJO_ORGS_JSON" | while IFS= read -r team; do
    team_body="$(jq -c --arg o "$org" --arg t "$team" '.[$o].teams[$t] + {name: $t}' "$FORGEJO_ORGS_JSON")"
    id="$(jq -r --arg t "$team" '.[] | select(.name == $t) | .id' <<<"$existing")"
    if [ -n "$id" ]; then
      api -X PATCH "$FORGEJO_API/teams/$id" -d "$team_body" >/dev/null
    else
      echo "forgejo-provision: creating team $org/$team"
      api -X POST "$FORGEJO_API/orgs/$org/teams" -d "$team_body" >/dev/null
    fi
  done
done
