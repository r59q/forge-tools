#!/bin/sh
# Checks the running agents API without starting a client turn.
set -eu
: "${FORGE_AGENTS_TEST_TOKEN:?set FORGE_AGENTS_TEST_TOKEN}"
url=${FORGE_AGENTS_TEST_URL:-http://127.0.0.1:8096}/v1/agents
root=""
child=""

api() { curl -fsS -H "Authorization: Bearer $FORGE_AGENTS_TEST_TOKEN" "$@"; }
cleanup() {
  if [ -n "$child" ]; then
    api -X DELETE "$url/$child" >/dev/null 2>&1 || true
  fi
  if [ -n "$root" ]; then
    api -X DELETE "$url/$root" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

i=0
until api "$url" >/dev/null 2>&1; do
  i=$((i + 1))
  [ "$i" -lt 60 ] || exit 1
  sleep 2
done

created=$(api "$url" -d '{"client":"codex"}')
root=$(echo "$created" | jq -er .id)
created=$(jq -n --arg root "$root" '{parent:$root,workspace:{kind:"shared"}}' | api "$url" -d @-)
child=$(echo "$created" | jq -er .id)
api "$url?root=$root&parent=$root&depth=1" |
  jq -e --arg root "$root" --arg child "$child" \
    '[.agents[].id] == [$child] and .agents[0].root == $root and .agents[0].depth == 1' >/dev/null
api "$url/$root/stop" -d '{}' >/dev/null
api "$url/$child" | jq -e '.state == "stopped"' >/dev/null
echo "ok: agent tree links and cascade stop"
