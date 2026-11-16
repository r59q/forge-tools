#!/bin/sh
# Smoke test of the search service: index a ready checkout, search the index
# and the checkout without it with the same query, then delete the index.
# FORGE_SEARCH_TEST_TOKEN, FORGE_SEARCH_TEST_CHECKOUT (a ready checkout ID)
# and FORGE_SEARCH_TEST_REVISION (its revision) are supplied by the caller,
# which also removes the checkout.
set -eu
: "${FORGE_SEARCH_TEST_TOKEN:?set FORGE_SEARCH_TEST_TOKEN}"
: "${FORGE_SEARCH_TEST_CHECKOUT:?set FORGE_SEARCH_TEST_CHECKOUT}"
: "${FORGE_SEARCH_TEST_REVISION:?set FORGE_SEARCH_TEST_REVISION}"
url=${FORGE_SEARCH_TEST_URL:-http://127.0.0.1:8095}
index=integration-test
api() { curl -fsS -H "Authorization: Bearer $FORGE_SEARCH_TEST_TOKEN" "$@"; }
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
# Start from no index, and leave none behind when a check fails.
api -X DELETE "$url/v1/indexes/$index" >/dev/null 2>&1 || true
trap 'api -X DELETE "$url/v1/indexes/$index" >/dev/null 2>&1 || true' EXIT

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
