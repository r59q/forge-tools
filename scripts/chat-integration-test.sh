#!/bin/sh
# Verify chat creation, archive and stop against the running full stack.
set -eu
: "${FORGE_CHAT_TEST_TOKEN:?set FORGE_CHAT_TEST_TOKEN}"
: "${FORGE_CHAT_TEST_AGENTS_TOKEN:?set FORGE_CHAT_TEST_AGENTS_TOKEN}"
url=http://127.0.0.1:8097
agents_url=http://127.0.0.1:8096

chat() {
  curl -fsS -H "Authorization: Bearer $FORGE_CHAT_TEST_TOKEN" -H "Content-Type: application/json" "$@"
}
agents() {
  curl -fsS -H "Authorization: Bearer $FORGE_CHAT_TEST_AGENTS_TOKEN" "$@"
}
cleanup() {
  if [ -n "$id" ]; then
    agents -X DELETE "$agents_url/v1/agents/$id" >/dev/null
  fi
}

i=0
until chat "$url/v1/agents" >/dev/null 2>&1; do
  i=$((i + 1))
  [ "$i" -lt 60 ] || { echo "chat did not start" >&2; exit 1; }
  sleep 2
done

curl -fsS "$url/" | grep -q '<title>forge-chat</title>'
id=""
trap cleanup EXIT
created=$(chat "$url/v1/agents" -d '{"client":"claude"}')
id=$(echo "$created" | jq -er '.id')
echo "$created" | jq -e '.state == "created" and .workspace.kind == "scratch"' >/dev/null
chat -X PATCH "$url/v1/chat-metadata/$id" -d '{"archived":true}' | jq -e '.archived' >/dev/null
chat "$url/v1/chat-metadata" | jq -e --arg id "$id" 'any(.chats[]?; .id == $id and .archived)' >/dev/null
agents "$agents_url/v1/agents/$id" | jq -e '.state == "created"' >/dev/null
chat "$url/v1/agents/$id/stop" -d '{}' | jq -e '.state == "stopped"' >/dev/null
echo "ok: chat shell, create, archive and stop"
