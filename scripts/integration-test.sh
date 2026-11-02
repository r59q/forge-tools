#!/bin/sh
# Tests the running full-stack example against a real forge repository.
#
# Usage: FORGE_TEST_TOKEN=<token> scripts/integration-test.sh
#   FORGE_TEST_TOKEN       forge token with read access to the repository
#   FORGE_TEST_FORGE       github (default), gitlab or forgejo
#   FORGE_TEST_HOST        forge host, such as git.example.com; defaults to
#                          github.com, gitlab.com or codeberg.org
#   FORGE_TEST_REPOSITORY  repository to read, default r59q/test-repo
#
# Start the stack first with scripts/fullstack.sh start. The test adds its
# own connection and removes it again. Needs curl and jq.
set -eu
: "${FORGE_TEST_TOKEN:?set FORGE_TEST_TOKEN}"
forge=${FORGE_TEST_FORGE:-github}
host=${FORGE_TEST_HOST:-}
repo=${FORGE_TEST_REPOSITORY:-r59q/test-repo}
url=http://127.0.0.1:8083
token=$(sed -n 's/^FORGE_MANAGEMENT_TOKEN=//p' "$(dirname "$0")/../examples/fullstack/.env")

checkout_token=$(sed -n 's/^FORGE_CHECKOUTS_TOKEN=//p' "$(dirname "$0")/../examples/fullstack/.env")
checkout_url=http://127.0.0.1:8088
checkout_ids=""
checkout_api() { curl -fsS -H "Authorization: Bearer $checkout_token" "$@"; }
api() { curl -fsS -H "Authorization: Bearer $token" "$@"; }
fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# Management has no health route; wait until it answers.
i=0
until api -o /dev/null "$url/v1/connections" 2>/dev/null; do
  i=$((i + 1))
  [ "$i" -lt 60 ] || fail "management did not start"
  sleep 2
done

# The forge token goes through stdin to keep it out of process arguments.
conn=$(jq -n --arg f "$forge" --arg h "$host" --arg c "$FORGE_TEST_TOKEN" \
  '{forge: $f, host: $h, name: "integration-test-\($f)", credential: $c}' |
  api "$url/v1/connections" -d @-) || fail "add $forge connection"
id=$(echo "$conn" | jq -r .id)
cleanup_checkouts() {
  # Includes failed attempts created while startup registration was pending.
  checkout_ids=$(checkout_api "$checkout_url/v1/checkouts" 2>/dev/null |
    jq -r --arg source "$id" '.checkouts[]? | select(.repository.source == $source) | .id') || true
  for checkout_id in $checkout_ids; do
    checkout_api -X DELETE "$checkout_url/v1/checkouts/$checkout_id" >/dev/null 2>&1 || true
  done
}
cleanup() {
  cleanup_checkouts
  api -X DELETE "$url/v1/connections/$id" >/dev/null 2>&1 || true
}
trap cleanup EXIT
echo "$conn" | jq -e '.status == "ok"' >/dev/null || fail "new connection is not ok"
echo "ok: add connection"

api -X POST "$url/v1/connections/$id/test" -d '{}' | jq -e '.status == "ok"' >/dev/null ||
  fail "test connection"
echo "ok: test connection"

api "$url/v1/repositories" |
  jq -e --arg id "$id" --arg r "$repo" 'any(.repositories[]?; .sourceId == $id and .path == $r)' >/dev/null ||
  fail "$repo not in repositories"
echo "ok: list repositories"

api "$url/v1/issues?state=all&source=$id&repository=$repo" |
  jq -e --arg r "$repo" '.configured and all(.issues[]?; .repository == $r)' >/dev/null ||
  fail "list issues of $repo"
echo "ok: list issues"

# Checkout creation uses the connector's authenticated Git route.
# Startup registration may still be finishing, so retry without leaving ready copies.
i=0
while :; do
  checkout=$(jq -n --arg source "$id" --arg repo "$repo" \
    '{repository:{source:$source,path:$repo},reference:"HEAD"}' |
    checkout_api "$checkout_url/v1/checkouts" -d @-) && break
  i=$((i + 1))
  [ "$i" -lt 30 ] || fail "create connected checkout"
  sleep 2
done
checkout_id=$(echo "$checkout" | jq -r .id)
checkout_ids="$checkout_id"
revision=$(echo "$checkout" | jq -r .revision)
echo "$checkout" | jq -e '.state == "ready" and (.revision | length > 0)' >/dev/null || fail "checkout not ready"
checkout_api "$checkout_url/v1/checkouts/$checkout_id/status" |
  jq -e --arg revision "$revision" '.revision == $revision and (.files | length == 0)' >/dev/null || fail "checkout status"
checkout_api "$checkout_url/v1/checkouts/$checkout_id/diff" |
  jq -e '(.staged // "") == "" and (.unstaged // "") == ""' >/dev/null || fail "checkout diff"
second=$(jq -n --arg source "$id" --arg repo "$repo" --arg revision "$revision" \
  '{repository:{source:$source,path:$repo},reference:$revision}' |
  checkout_api "$checkout_url/v1/checkouts" -d @-) || fail "create checkout at commit"
second_id=$(echo "$second" | jq -r .id)
checkout_ids="$checkout_ids $second_id"
[ "$checkout_id" != "$second_id" ] || fail "checkout IDs reused"
checkout_api -X POST "$checkout_url/v1/checkouts/$checkout_id/retain" -d '{}' |
  jq -e '.state == "retained"' >/dev/null || fail "retain checkout"
for checkout_id in $checkout_ids; do
  checkout_api -X DELETE "$checkout_url/v1/checkouts/$checkout_id" >/dev/null || fail "remove checkout"
done
checkout_ids=""
# Remove failed startup attempts belonging to this test connection as well.
cleanup_checkouts
echo "ok: connected checkout create, inspect, diff, retain and remove"

api -X DELETE "$url/v1/connections/$id" >/dev/null || fail "remove connection"
trap - EXIT
api "$url/v1/connections" | jq -e --arg id "$id" 'all(.connections[]?; .id != $id)' >/dev/null ||
  fail "connection still listed after removal"
echo "ok: remove connection"
