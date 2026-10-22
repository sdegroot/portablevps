# Protect every repository's default branch so it only accepts verified
# commits. Runs from a timer, so repositories created after a deploy are
# covered too.
#
# Additive, like the rest of provisioning: a missing rule is created (pushes
# stay open to everyone with write access, nothing else is required), an
# existing rule only gets require_signed_commits switched on, and nothing is
# ever removed. Archived repositories and pull mirrors cannot be pushed to and
# are skipped; an empty repository gets its rule before its first push.
#
# Inputs (environment):
#   FORGEJO_API                  e.g. http://127.0.0.1:3000/api/v1
#   FORGEJO_ADMIN_USER           break-glass admin username (sees every repository)
#   FORGEJO_ADMIN_PASSWORD_FILE  file holding its password
#   EXCLUDE_JSON                 ["owner/repo", ...] never protected
set -euo pipefail

: "${FORGEJO_API:?}" "${FORGEJO_ADMIN_USER:?}" "${FORGEJO_ADMIN_PASSWORD_FILE:?}" "${EXCLUDE_JSON:?}"

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
curl_cfg="$workdir/curl.cfg"
( umask 077; : > "$curl_cfg" )
escape() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }
printf 'user = "%s:%s"\n' "$(escape "$FORGEJO_ADMIN_USER")" "$(escape "$(tr -d '\n' < "$FORGEJO_ADMIN_PASSWORD_FILE")")" > "$curl_cfg"

api() { curl -sS --fail-with-body -K "$curl_cfg" -H 'Content-Type: application/json' "$@"; }
status() { curl -sS -o /dev/null -w '%{http_code}' -K "$curl_cfg" "$@"; }

# pages <path> <jq filter>: every page of a list endpoint, filtered.
pages() {
  local page=1 batch
  while :; do
    batch="$(api "$FORGEJO_API$1?limit=50&page=$page")"
    [ "$(jq length <<<"$batch")" -gt 0 ] || break
    jq -r "$2" <<<"$batch"
    page=$((page + 1))
  done
}

# Every owner, then every repository: /repos/search only returns what the
# admin could see as a member, which misses private repositories of users and
# of organizations the admin does not own. All of it is read before the first
# rule is written.
owners="$workdir/owners"
repos="$workdir/repos.tsv"
{ pages /admin/users '.[].login'; pages /admin/orgs '.[].username'; } > "$owners"
while IFS= read -r owner; do
  pages "/users/$owner/repos" '.[] | select((.archived or .mirror) | not) | [.full_name, .default_branch] | @tsv'
done < "$owners" > "$repos"

failed=0
while IFS=$'\t' read -r repo branch; do
  if jq -e --arg r "$repo" 'index($r) != null' "$EXCLUDE_JSON" >/dev/null; then
    continue
  fi
  if [ -z "$branch" ]; then
    echo "forgejo-require-signed-commits: $repo has no default branch; skipped" >&2
    continue
  fi
  rule="$(jq -rn --arg b "$branch" '$b | @uri')"
  code="$(status "$FORGEJO_API/repos/$repo/branch_protections/$rule")"
  case "$code" in
    404)
      echo "forgejo-require-signed-commits: protecting $repo:$branch"
      api -X POST "$FORGEJO_API/repos/$repo/branch_protections" -d "$(jq -nc --arg b "$branch" \
        '{rule_name: $b, enable_push: true, require_signed_commits: true}')" >/dev/null || failed=1
      ;;
    200)
      if [ "$(api "$FORGEJO_API/repos/$repo/branch_protections/$rule" | jq -r .require_signed_commits)" != true ]; then
        echo "forgejo-require-signed-commits: requiring signed commits on the existing rule for $repo:$branch"
        api -X PATCH "$FORGEJO_API/repos/$repo/branch_protections/$rule" -d '{"require_signed_commits": true}' >/dev/null || failed=1
      fi
      ;;
    *)
      echo "forgejo-require-signed-commits: GET branch protection $repo:$branch returned HTTP $code" >&2
      failed=1
      ;;
  esac
done < "$repos"

# One broken repository must not leave the others unprotected, but the run
# still fails so it shows up as a failed unit.
exit "$failed"
