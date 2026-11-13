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

# Explorer uses its own configured sample root, independent of forge/checkout state.
FORGE_EXPLORER_TEST_TOKEN=$(sed -n 's/^FORGE_EXPLORER_TOKEN=//p' "$(dirname "$0")/../examples/fullstack/.env") \
  sh "$(dirname "$0")/explorer-integration-test.sh"

# Pipeprint compiles request content only, independent of forge state.
FORGE_PIPEPRINT_TEST_TOKEN=$(sed -n 's/^FORGE_PIPEPRINT_TOKEN=//p' "$(dirname "$0")/../examples/fullstack/.env") \
  sh "$(dirname "$0")/pipeprint-integration-test.sh"

# Symbols analyzes request content only, independent of forge state.
FORGE_SYMBOLS_TEST_TOKEN=$(sed -n 's/^FORGE_SYMBOLS_TOKEN=//p' "$(dirname "$0")/../examples/fullstack/.env") \
  sh "$(dirname "$0")/symbols-integration-test.sh"

# The reactor uses its configured webhook source and RabbitMQ, independent of forge state.
env_file="$(dirname "$0")/../examples/fullstack/.env"
FORGE_REACTOR_TEST_TOKEN=$(sed -n 's/^FORGE_REACTOR_TOKEN=//p' "$env_file") \
  FORGE_REACTOR_TEST_WEBHOOK_SECRET=$(sed -n 's/^FORGE_REACTOR_WEBHOOK_SECRET=//p' "$env_file") \
  FORGE_REACTOR_TEST_RABBITMQ_PASSWORD=$(sed -n 's/^FORGE_REACTOR_RABBITMQ_PASSWORD=//p' "$env_file") \
  sh "$(dirname "$0")/reactor-integration-test.sh"

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

# Managed webhook sources need a GitHub or Forgejo connection.
if [ "$forge" != gitlab ]; then
  FORGE_WEBHOOKS_TEST_TOKEN=$token FORGE_WEBHOOKS_TEST_CONNECTION=$id \
    FORGE_WEBHOOKS_TEST_FORGE=$forge FORGE_WEBHOOKS_TEST_REPOSITORY=$repo \
    sh "$(dirname "$0")/webhooks-integration-test.sh"
fi

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

# Pipeline browsing uses the same source and repository as the other tools.
actions_token=$(sed -n 's/^FORGE_ACTIONS_TOKEN=//p' "$(dirname "$0")/../examples/fullstack/.env")
actions_url=http://127.0.0.1:8089
actions_api() { curl -fsS -H "Authorization: Bearer $actions_token" "$@"; }
pipelines=$(api --get "$url/v1/pipelines" \
  --data-urlencode "source=$id" --data-urlencode "repository=$repo") ||
  fail "list pipelines"
echo "$pipelines" | jq -e --arg source "$id" --arg repo "$repo" \
  '.configured and all(.pipelines[]?; .ref.source == $source and .ref.repository == $repo and (.ref.id | length > 0))' \
  >/dev/null || fail "pipeline identities"
pipeline_id=$(echo "$pipelines" | jq -r '.pipelines[0].ref.id // empty')
if [ -n "$pipeline_id" ]; then
  api --get "$url/v1/pipelines/$pipeline_id" \
    --data-urlencode "source=$id" --data-urlencode "repository=$repo" |
    jq -e --arg id "$pipeline_id" '.ref.id == $id' >/dev/null || fail "get pipeline"
  jobs=$(api --get "$url/v1/pipelines/$pipeline_id/jobs" \
    --data-urlencode "source=$id" --data-urlencode "repository=$repo") ||
    fail "list pipeline jobs"
  echo "$jobs" | jq -e --arg id "$pipeline_id" 'all(.jobs[]?; .pipelineId == $id)' \
    >/dev/null || fail "job pipeline identities"
  job_id=$(echo "$jobs" | jq -r '[.jobs[]? | select(.status.phase == "finished")][0].ref.id // empty')
  if [ -n "$job_id" ]; then
    api --get "$url/v1/jobs/$job_id" \
      --data-urlencode "source=$id" --data-urlencode "repository=$repo" |
      jq -e --arg id "$job_id" '.ref.id == $id' >/dev/null || fail "get job"

    # Native retention may have removed a completed job's log.
    log_file=$(mktemp)
    log_code=$(curl -sS -o "$log_file" -w '%{http_code}' -H "Authorization: Bearer $actions_token" \
      --get "$actions_url/v1/jobs/$job_id/log" \
      --data-urlencode "source=$id" --data-urlencode "repository=$repo") || {
      rm -f "$log_file"
      fail "read job log"
    }
    case "$log_code" in
      200)
        jq -e '(.text | type == "string") and (.truncated | type == "boolean")' "$log_file" >/dev/null || {
          rm -f "$log_file"
          fail "job log shape"
        }
        ;;
      404)
        echo "skip: job log has expired or is unavailable"
        ;;
      *)
        rm -f "$log_file"
        fail "job log HTTP $log_code"
        ;;
    esac
    rm -f "$log_file"
  fi

  artifacts=$(actions_api --get "$actions_url/v1/pipelines/$pipeline_id/artifacts" \
    --data-urlencode "source=$id" --data-urlencode "repository=$repo") ||
    fail "list artifacts"
  echo "$artifacts" | jq -e --arg id "$pipeline_id" 'all(.artifacts[]?; .pipelineId == $id)' \
    >/dev/null || fail "artifact pipeline identities"
  artifact_id=$(echo "$artifacts" | jq -r '[.artifacts[]? | select(.availability == "available" and (.size | tonumber) >= 0 and (.size | tonumber) <= 16777216)][0].ref.id // empty')
  if [ -n "$artifact_id" ]; then
    actions_api --get "$actions_url/v1/artifacts/$artifact_id/download" \
      --data-urlencode "source=$id" --data-urlencode "repository=$repo" |
      jq -e '.data | type == "string"' >/dev/null || fail "download artifact"
  fi
else
  echo "skip: no pipeline executions in this repository"
fi
echo "ok: pipeline browsing"

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
  '{repository:{source:$source,path:$repo},reference:$revision,mode:"isolated"}' |
  checkout_api "$checkout_url/v1/checkouts" -d @-) || fail "create isolated checkout at commit"
second_id=$(echo "$second" | jq -r .id)
checkout_ids="$checkout_ids $second_id"
[ "$checkout_id" != "$second_id" ] || fail "checkout IDs reused"
echo "$second" | jq -e '.mode == "isolated"' >/dev/null || fail "isolated checkout mode"
checkout_api "$checkout_url/v1/checkouts/$second_id/status" |
  jq -e --arg revision "$revision" '.revision == $revision and (.files | length == 0)' >/dev/null || fail "isolated checkout status"
checkout_api "$checkout_url/v1/checkouts/$second_id/diff" |
  jq -e '(.staged // "") == "" and (.unstaged // "") == ""' >/dev/null || fail "isolated checkout diff"
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
