#!/bin/sh
# Tests managed webhook sources: management adds one to the reactor, which
# accepts deliveries signed with its secret, rejects them after a rotation and
# forgets the source once removed. The caller supplies a GitHub or Forgejo
# connection. Needs curl, jq and openssl.
#
#   FORGE_WEBHOOKS_TEST_TOKEN       management operator token
#   FORGE_WEBHOOKS_TEST_CONNECTION  connection ID
#   FORGE_WEBHOOKS_TEST_FORGE       github or forgejo
#   FORGE_WEBHOOKS_TEST_REPOSITORY  owner/name the source accepts
set -eu
: "${FORGE_WEBHOOKS_TEST_TOKEN:?set FORGE_WEBHOOKS_TEST_TOKEN}"
: "${FORGE_WEBHOOKS_TEST_CONNECTION:?set FORGE_WEBHOOKS_TEST_CONNECTION}"
: "${FORGE_WEBHOOKS_TEST_FORGE:?set FORGE_WEBHOOKS_TEST_FORGE}"
: "${FORGE_WEBHOOKS_TEST_REPOSITORY:?set FORGE_WEBHOOKS_TEST_REPOSITORY}"
management=${FORGE_WEBHOOKS_TEST_MANAGEMENT_URL:-http://127.0.0.1:8083}
reactor=${FORGE_WEBHOOKS_TEST_REACTOR_URL:-http://127.0.0.1:8093}
api() { curl -fsS -H "Authorization: Bearer $FORGE_WEBHOOKS_TEST_TOKEN" "$@"; }
fail() {
  echo "FAIL: $*" >&2
  exit 1
}

source="it-hook-$(openssl rand -hex 4)"
cleanup() { api -X DELETE "$management/v1/webhooks/$source" >/dev/null 2>&1 || true; }
trap cleanup EXIT

added=$(jq -n --arg id "$source" --arg c "$FORGE_WEBHOOKS_TEST_CONNECTION" --arg r "$FORGE_WEBHOOKS_TEST_REPOSITORY" \
  '{id: $id, connection: $c, repositories: [$r]}' | api "$management/v1/webhooks" -d @-) || fail "add webhook source"
secret=$(printf '%s\n' "$added" | jq -r .secret)
printf '%s\n' "$added" | jq -e --arg f "$FORGE_WEBHOOKS_TEST_FORGE" '.webhook.managed and .webhook.forge == $f' >/dev/null ||
  fail "added webhook source"
api "$management/v1/webhooks" | jq -e --arg id "$source" 'any(.webhooks[]; .id == $id) and (tostring | contains("secret") | not)' \
  >/dev/null || fail "list webhook sources"
echo "ok: management adds a webhook source with a generated secret"

body=$(jq -cn --arg r "$FORGE_WEBHOOKS_TEST_REPOSITORY" \
  '{ref: "refs/heads/main", before: ("1" * 40), after: ("2" * 40), repository: {full_name: $r}, pusher: {name: "it"}}')

# deliver secret delivery-id prints the reactor's status for a signed push.
deliver() {
  signature=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$1" | sed 's/.* //')
  if [ "$FORGE_WEBHOOKS_TEST_FORGE" = github ]; then
    set -- -H 'X-GitHub-Event: push' -H "X-GitHub-Delivery: $2" -H "X-Hub-Signature-256: sha256=$signature"
  else
    set -- -H 'X-Forgejo-Event: push' -H "X-Forgejo-Delivery: $2" -H "X-Forgejo-Signature: $signature"
  fi
  curl -sS -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' "$@" \
    "$reactor/webhooks/$source" -d "$body"
}

[ "$(deliver "$secret" "$source-1")" = 202 ] || fail "signed delivery"
echo "ok: reactor accepts deliveries signed with the generated secret"

rotated=$(api -X POST "$management/v1/webhooks/$source:rotate" -d '{}' | jq -r .secret) || fail "rotate secret"
[ "$rotated" != "$secret" ] || fail "rotation kept the secret"
[ "$(deliver "$secret" "$source-2")" = 401 ] || fail "old secret accepted after rotation"
[ "$(deliver "$rotated" "$source-3")" = 202 ] || fail "rotated secret"
echo "ok: rotation replaces the secret at once"

api -X DELETE "$management/v1/webhooks/$source" | jq -e '(.failedRepositories // []) == []' >/dev/null ||
  fail "remove webhook source"
[ "$(deliver "$rotated" "$source-4")" = 404 ] || fail "removed source still accepts deliveries"
echo "ok: removed webhook sources reject deliveries"
