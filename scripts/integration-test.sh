#!/bin/sh
# Tests the running full-stack example against a real GitHub repository.
#
# Usage: FORGE_TEST_TOKEN=<token> scripts/integration-test.sh
#   FORGE_TEST_TOKEN       GitHub token with read access to the repository
#   FORGE_TEST_REPOSITORY  repository to read, default r59q/test-repo
#
# Start the stack first with scripts/fullstack.sh start. The test adds its
# own connection and removes it again. Needs curl and jq.
set -eu
: "${FORGE_TEST_TOKEN:?set FORGE_TEST_TOKEN}"
repo=${FORGE_TEST_REPOSITORY:-r59q/test-repo}
url=http://127.0.0.1:8083
token=$(sed -n 's/^FORGE_MANAGEMENT_TOKEN=//p' "$(dirname "$0")/../examples/fullstack/.env")

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
conn=$(jq -n --arg c "$FORGE_TEST_TOKEN" '{forge: "github", name: "integration-test", credential: $c}' |
  api "$url/v1/connections" -d @-) || fail "add connection"
id=$(echo "$conn" | jq -r .id)
trap 'api -X DELETE "$url/v1/connections/$id" >/dev/null 2>&1 || true' EXIT
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

api -X DELETE "$url/v1/connections/$id" >/dev/null || fail "remove connection"
trap - EXIT
api "$url/v1/connections" | jq -e --arg id "$id" 'all(.connections[]?; .id != $id)' >/dev/null ||
  fail "connection still listed after removal"
echo "ok: remove connection"
