#!/bin/sh
# Smoke test of the search service: index a ready checkout, search the index
# and the checkout without it with the same query, index the default branch
# of its repository and search it with and without that index, then delete
# the indexes. FORGE_SEARCH_TEST_TOKEN, FORGE_SEARCH_TEST_CHECKOUT (a ready
# checkout of the default branch), FORGE_SEARCH_TEST_REVISION (its revision),
# FORGE_SEARCH_TEST_SOURCE and FORGE_SEARCH_TEST_REPOSITORY (its repository),
# and FORGE_SEARCH_TEST_CHECKOUTS_TOKEN (the forge-checkouts token, to check no
# checkout is left) are supplied by the caller, which also removes the checkout.
set -eu
: "${FORGE_SEARCH_TEST_TOKEN:?set FORGE_SEARCH_TEST_TOKEN}"
: "${FORGE_SEARCH_TEST_CHECKOUT:?set FORGE_SEARCH_TEST_CHECKOUT}"
: "${FORGE_SEARCH_TEST_REVISION:?set FORGE_SEARCH_TEST_REVISION}"
: "${FORGE_SEARCH_TEST_SOURCE:?set FORGE_SEARCH_TEST_SOURCE}"
: "${FORGE_SEARCH_TEST_REPOSITORY:?set FORGE_SEARCH_TEST_REPOSITORY}"
: "${FORGE_SEARCH_TEST_CHECKOUTS_TOKEN:?set FORGE_SEARCH_TEST_CHECKOUTS_TOKEN}"
: "${FORGE_SEARCH_TEST_CHECKOUTS_URL:=http://127.0.0.1:8088}"
url=${FORGE_SEARCH_TEST_URL:-http://127.0.0.1:8095}
index=integration-test
repo_index=""
api() { curl -fsS -H "Authorization: Bearer $FORGE_SEARCH_TEST_TOKEN" "$@"; }
checkouts_api() { curl -fsS -H "Authorization: Bearer $FORGE_SEARCH_TEST_CHECKOUTS_TOKEN" "$@"; }
fail() {
  echo "FAIL: $*" >&2
  exit 1
}
# The service has no health route; wait until listing indexes answers.
i=0
until api -o /dev/null "$url/v1/indexes" 2>/dev/null; do
  i=$((i + 1))
  [ "$i" -lt 60 ] || fail "search did not start"
  sleep 2
done
# Start from no index, and leave none behind when a check fails. The
# repository index is named by the service, so it is deleted once known.
cleanup() {
  for i in "$index" $repo_index; do
    api -X DELETE "$url/v1/indexes/$i" >/dev/null 2>&1 || true
  done
}
cleanup
trap cleanup EXIT

jq -n --arg c "$FORGE_SEARCH_TEST_CHECKOUT" '{checkoutId: $c}' |
  api "$url/v1/indexes/$index/checkout" -d @- |
  jq -e --arg i "$index" --arg c "$FORGE_SEARCH_TEST_CHECKOUT" --arg r "$FORGE_SEARCH_TEST_REVISION" \
    '.index.name == $i and (.index.documents | tonumber) > 0 and
      .index.revision == $r and .index.checkoutId == $c' >/dev/null ||
  fail "index checkout"
api "$url/v1/indexes/$index" |
  jq -e --arg r "$FORGE_SEARCH_TEST_REVISION" '.index.revision == $r' >/dev/null || fail "get index"
echo "ok: search indexes the checkout at its revision"

# The test repository is configurable, so compare the two modes on a query
# that nearly any text file matches rather than on known content.
query='{query: {text: "e", maxResults: 500}}'
indexed=$(jq -n "$query" | api "$url/v1/indexes/$index:search" -d @-) || fail "search index"
unindexed=$(jq -n "$query" | api "$url/v1/checkouts/$FORGE_SEARCH_TEST_CHECKOUT:search" -d @-) ||
  fail "search checkout"
# printf, not echo: some sh echo builtins expand the backslashes of JSON escapes.
printf '%s\n' "$unindexed" | jq -e '.files.truncated != true' >/dev/null ||
  fail "checkout is over the unindexed search limits"
# Through stdin: the answers can exceed the size of one argument.
printf '%s\n%s\n' "$indexed" "$unindexed" |
  jq -s -e --arg r "$FORGE_SEARCH_TEST_REVISION" '.[0] as $a | .[1] as $b |
    ($a.matches | length) > 0 and $a.revision == $r and $b.revision == $r and
    $a.matches == $b.matches and ($a.state // {}) == ($b.state // {})' >/dev/null ||
  fail "indexed and unindexed searches differ"
echo "ok: search finds the same matches with and without the index"

# Repository indexes check the default branch out and remove the checkout,
# so the checkouts of the source must be the caller's alone afterwards.
target=$(jq -n -c --arg s "$FORGE_SEARCH_TEST_SOURCE" --arg r "$FORGE_SEARCH_TEST_REPOSITORY" \
  '{source: $s, repository: $r}')
own_checkouts() {
  checkouts_api "$FORGE_SEARCH_TEST_CHECKOUTS_URL/v1/checkouts" |
    jq -c --arg s "$FORGE_SEARCH_TEST_SOURCE" '[.checkouts[]? | select(.repository.source == $s) | .id] | sort'
}
before=$(own_checkouts) || fail "list checkouts"
repository=$(jq -n --argjson t "$target" '{target: $t}' | api "$url/v1/repositories:index" -d @-) ||
  fail "index repository"
repo_index=$(printf '%s\n' "$repository" | jq -r '.index.name')
printf '%s\n' "$repository" |
  jq -e --arg r "$FORGE_SEARCH_TEST_REVISION" --argjson t "$target" \
    '(.index.name | startswith("repo-")) and (.index.documents | tonumber) > 0 and
      .index.revision == $r and .index.target == $t and (.upToDate // false) == false' >/dev/null ||
  fail "index repository default branch"
jq -n --argjson t "$target" '{target: $t}' | api "$url/v1/repositories:index" -d @- |
  jq -e --arg r "$FORGE_SEARCH_TEST_REVISION" '.upToDate == true and .index.revision == $r' >/dev/null ||
  fail "reindex unchanged repository"
echo "ok: search indexes the default branch and finds it up to date"

repo_query=$(jq -n -c --argjson t "$target" "$query + {target: \$t}")
by_index=$(api "$url/v1/repositories:search" -d "$repo_query") || fail "search repository index"
api -X DELETE "$url/v1/indexes/$repo_index" >/dev/null || fail "delete repository index"
by_checkout=$(api "$url/v1/repositories:search" -d "$repo_query") || fail "search repository checkout"
printf '%s\n%s\n%s\n' "$indexed" "$by_index" "$by_checkout" |
  jq -s -e --arg r "$FORGE_SEARCH_TEST_REVISION" --arg i "$repo_index" '.[0] as $a | .[1] as $b | .[2] as $c |
    $b.index.name == $i and $b.revision == $r and $c.index == null and $c.revision == $r and
    $b.matches == $a.matches and $c.matches == $a.matches' >/dev/null ||
  fail "repository searches differ"
[ "$(own_checkouts)" = "$before" ] || fail "repository checkouts left behind"
echo "ok: search finds the same matches in a repository with and without its index"

[ "$(curl -sS -o /dev/null -w '%{http_code}' "$url/v1/indexes")" = 401 ] ||
  fail "unauthenticated request"
echo "ok: search authentication"

api -X DELETE "$url/v1/indexes/$index" >/dev/null || fail "delete index"
[ "$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $FORGE_SEARCH_TEST_TOKEN" \
  "$url/v1/indexes/$index")" = 404 ] || fail "index still exists after deletion"
api "$url/v1/indexes" | jq -e --arg i "$index" 'all(.indexes[]?; .name != $i)' >/dev/null ||
  fail "index still listed after deletion"
trap - EXIT
echo "ok: search deletes the index"
