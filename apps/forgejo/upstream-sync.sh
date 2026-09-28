# Bring every Forgejo "upstream copy" up to date with its upstream repository.
#
# Upstream owns every ref except branches under the private prefix, so per copy:
#   - the default branch and tags move fast-forward only: if upstream rewrote
#     them, the push is rejected and this run fails loudly instead of rewriting
#     history that people may have built on;
#   - other branches follow upstream exactly (rebased feature branches too);
#   - refs deleted upstream are deleted here;
#   - branches under the private prefix are never read from upstream, pushed,
#     or deleted (the pre-receive hook would refuse it anyway).
# Only refs that differ are pushed. One failing copy does not stop the others;
# the run exits non-zero if any copy failed.
#
# Inputs (environment):
#   FORGEJO_URL           e.g. http://127.0.0.1:3000
#   UPSTREAM_TOKEN_FILE   the sync bot's API token
#   UPSTREAM_COPIES_JSON  { "owner/repo": { upstream, privatePrefix } }
#   UPSTREAM_CACHE_DIR    where the bare fetch caches live
set -euo pipefail

: "${FORGEJO_URL:?}" "${UPSTREAM_TOKEN_FILE:?}" "${UPSTREAM_COPIES_JSON:?}" "${UPSTREAM_CACHE_DIR:?}"

# The token reaches git through its environment config, never argv (ps).
export GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0="http.${FORGEJO_URL}/.extraHeader"
GIT_CONFIG_VALUE_0="Authorization: token $(tr -d '\n' < "$UPSTREAM_TOKEN_FILE")"
export GIT_CONFIG_VALUE_0
export GIT_TERMINAL_PROMPT=0

failed=0
while IFS= read -r full; do
  upstream="$(jq -r --arg k "$full" '.[$k].upstream' "$UPSTREAM_COPIES_JSON")"
  prefix="$(jq -r --arg k "$full" '.[$k].privatePrefix' "$UPSTREAM_COPIES_JSON")"
  target="$FORGEJO_URL/$full.git"
  cache="$UPSTREAM_CACHE_DIR/$full.git"

  if ! {
    [ -d "$cache" ] || git init -q --bare "$cache"
    git -C "$cache" fetch -q --prune --no-tags "$upstream" \
      "+refs/heads/*:refs/up/heads/*" "+refs/tags/*:refs/up/tags/*" "^refs/heads/$prefix*"
    default_branch="$(git ls-remote --symref "$upstream" HEAD | sed -n 's#^ref: refs/heads/\(.*\)\tHEAD$#\1#p')"

    # want: upstream refs, as they should appear in Forgejo; have: Forgejo now.
    git -C "$cache" for-each-ref --format='%(objectname) %(refname)' refs/up \
      | sed -e 's# refs/up/# refs/#' | sort -k2 > "$cache/want"
    git ls-remote --heads --tags --refs "$target" | awk '{print $1" "$2}' \
      | grep -v " refs/heads/$prefix" | sort -k2 > "$cache/have" || true

    specs=()
    while read -r sha ref; do
      current="$(awk -v r="$ref" '$2 == r {print $1}' "$cache/have")"
      [ "$current" = "$sha" ] && continue
      src="refs/up/${ref#refs/}"
      case "$ref" in
        "refs/heads/$default_branch" | refs/tags/*) specs+=("$src:$ref") ;;   # fast-forward only
        *) specs+=("+$src:$ref") ;;
      esac
    done < "$cache/want"
    while read -r _ ref; do
      awk -v r="$ref" '$2 == r {found = 1} END {exit !found}' "$cache/want" || specs+=(":$ref")  # deleted upstream
    done < "$cache/have"

    if [ "${#specs[@]}" -gt 0 ]; then
      echo "$full: pushing ${#specs[@]} ref update(s)"
      git -C "$cache" push -q "$target" "${specs[@]}"
    fi
  }; then
    echo "$full: sync FAILED" >&2
    failed=1
  fi
done < <(jq -r 'keys[]' "$UPSTREAM_COPIES_JSON")

exit "$failed"
