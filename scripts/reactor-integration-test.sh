#!/bin/sh
# Smoke test of the reactor service and RabbitMQ: subscribe, publish, consume
# with reject and ack, ingest a signed webhook, replay and authentication.
# FORGE_REACTOR_TEST_TOKEN, FORGE_REACTOR_TEST_WEBHOOK_SECRET and
# FORGE_REACTOR_TEST_RABBITMQ_PASSWORD are supplied by the caller. Messages
# are read through the RabbitMQ management API. Needs curl, jq and openssl.
set -eu
: "${FORGE_REACTOR_TEST_TOKEN:?set FORGE_REACTOR_TEST_TOKEN}"
: "${FORGE_REACTOR_TEST_WEBHOOK_SECRET:?set FORGE_REACTOR_TEST_WEBHOOK_SECRET}"
: "${FORGE_REACTOR_TEST_RABBITMQ_PASSWORD:?set FORGE_REACTOR_TEST_RABBITMQ_PASSWORD}"
url=${FORGE_REACTOR_TEST_URL:-http://127.0.0.1:8093}
mq=${FORGE_REACTOR_TEST_RABBITMQ_API:-http://127.0.0.1:15672}
api() { curl -fsS -H "Authorization: Bearer $FORGE_REACTOR_TEST_TOKEN" "$@"; }
# The password goes through stdin to keep it out of process arguments.
mq_api() { printf 'user = "reactor:%s"\n' "$FORGE_REACTOR_TEST_RABBITMQ_PASSWORD" | curl -fsS -K - "$@"; }
fail() {
  echo "FAIL: $*" >&2
  exit 1
}
wait_for() {
  i=0
  until "$@"; do
    i=$((i + 1))
    [ "$i" -lt 60 ] || return 1
    sleep 2
  done
}
wait_for api -o /dev/null "$url/v1/subscriptions" 2>/dev/null || fail "reactor did not start"
wait_for mq_api -o /dev/null "$mq/api/overview" 2>/dev/null || fail "rabbitmq did not start"

suffix=$(openssl rand -hex 4)
sub="it-$suffix"
type="it.reactor.$suffix"
queue="forge-reactor.$sub"
cleanup() { api -X DELETE "$url/v1/subscriptions/$sub" >/dev/null 2>&1 || true; }
trap cleanup EXIT

# Takes one message from the queue; mode is reject_requeue_true or ack_requeue_false.
take() {
  jq -n --arg mode "$1" '{count: 1, ackmode: $mode, encoding: "auto"}' |
    mq_api -H 'Content-Type: application/json' "$mq/api/queues/%2F/$queue/get" -d @-
}
take_one() {
  i=0
  while :; do
    # The queue appears once the reactor has declared it.
    message=$(take "$1" 2>/dev/null) || message="[]"
    [ "$(printf '%s\n' "$message" | jq length)" = 0 ] || break
    i=$((i + 1))
    [ "$i" -lt 60 ] || return 1
    sleep 1
  done
  printf '%s\n' "$message" | jq '.[0]'
}

jq -n --arg id "$sub" --arg t "$type" \
  '{id: $id, filter: {types: [$t, "forge.branch.updated"], origins: ["published", "webhook"]}}' |
  api "$url/v1/subscriptions" -d @- | jq -e --arg id "$sub" '.id == $id' >/dev/null ||
  fail "create subscription"

publish() {
  jq -n --arg t "$type" --arg k "it-$suffix" \
    '{type: $t, repository: "it/repo", subject: "builds/1", data: {ok: true}, idempotencyKey: $k}' |
    api "$url/v1/events" -d @-
}
first=$(publish) || fail "publish"
event=$(printf '%s\n' "$first" | jq -r '.event.id')
printf '%s\n' "$first" | jq -e '.event.origin == "published" and (.duplicate | not)' >/dev/null || fail "published event"
publish | jq -e --arg id "$event" '.event.id == $id and .duplicate' >/dev/null || fail "idempotent publish"
echo "ok: reactor publishes once per idempotency key"

# A rejected message is redelivered with the same event ID.
take_one reject_requeue_true | jq -e --arg id "$event" '.properties.message_id == $id and (.redelivered | not)' \
  >/dev/null || fail "first delivery"
take_one ack_requeue_false |
  jq -e --arg id "$event" '.redelivered and (.payload | fromjson | .id == $id and .data.ok)' >/dev/null ||
  fail "redelivery after reject"
api "$url/v1/deliveries?subscription=$sub&status=broker_accepted" |
  jq -e --arg id "$event" 'any(.deliveries[]; .eventId == $id and (.attempts | length >= 1))' >/dev/null ||
  fail "delivery history"
echo "ok: reactor delivers through rabbitmq with reject and redelivery"

body=$(jq -cn --arg r "it/repo-$suffix" \
  '{ref: "refs/heads/main", before: ("1" * 40), after: ("2" * 40), repository: {full_name: $r}, pusher: {name: "it"}}')
signature=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$FORGE_REACTOR_TEST_WEBHOOK_SECRET" | sed 's/.* //')
hook() {
  curl -sS -o "$hook_file" -w '%{http_code}' -H 'Content-Type: application/json' \
    -H 'X-GitHub-Event: push' -H "X-GitHub-Delivery: it-$suffix" -H "X-Hub-Signature-256: sha256=$1" \
    "$url/webhooks/example" -d "$body"
}
hook_file=$(mktemp)
trap 'rm -f "$hook_file"; cleanup' EXIT
[ "$(hook "$signature")" = 202 ] || fail "signed webhook"
hook_event=$(jq -r .event "$hook_file")
[ "$(hook "$signature")" = 202 ] && jq -e --arg id "$hook_event" '.event == $id and .duplicate' "$hook_file" >/dev/null ||
  fail "duplicate webhook"
wrong=$(printf "%s" "$body" | openssl dgst -sha256 -hmac not-the-secret | sed "s/.* //")
[ "$(hook "$wrong")" = 401 ] || fail "badly signed webhook"
take_one ack_requeue_false |
  jq -e --arg id "$hook_event" '.payload | fromjson |
    .id == $id and .type == "forge.branch.updated" and .origin == "webhook" and .connection == "example" and .data.branch == "main"' \
  >/dev/null || fail "webhook delivery"
echo "ok: reactor ingests signed webhooks once and rejects bad signatures"

jq -n --arg id "$event" '{eventIds: [$id]}' | api "$url/v1/subscriptions/$sub:replay" -d @- |
  jq -e --arg id "$event" '.deliveries[0].eventId == $id and .deliveries[0].replay' >/dev/null || fail "replay"
take_one ack_requeue_false |
  jq -e --arg id "$event" '.properties.message_id == $id and .properties.headers["forge-reactor-replay"] == true' \
  >/dev/null || fail "replayed delivery"
echo "ok: reactor replays retained events with their identity"

[ "$(curl -sS -o /dev/null -w '%{http_code}' "$url/v1/subscriptions")" = 401 ] || fail "unauthenticated request"
api -X DELETE "$url/v1/subscriptions/$sub" >/dev/null || fail "delete subscription"
[ "$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $FORGE_REACTOR_TEST_TOKEN" \
  "$url/v1/subscriptions/$sub")" = 404 ] || fail "deleted subscription still listed"
echo "ok: reactor authentication and subscription removal"
