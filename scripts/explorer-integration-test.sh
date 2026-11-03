#!/bin/sh
# Read-only smoke test of the explorer and the full-stack sample content root.
# FORGE_EXPLORER_TEST_TOKEN is supplied by the caller; this script reads no token files.
set -eu
: "${FORGE_EXPLORER_TEST_TOKEN:?set FORGE_EXPLORER_TEST_TOKEN}"
explorer_url=${FORGE_EXPLORER_TEST_URL:-http://127.0.0.1:8090}
explorer_api() { curl -fsS -H "Authorization: Bearer $FORGE_EXPLORER_TEST_TOKEN" "$@"; }
i=0
until sources=$(explorer_api "$explorer_url/v1/explorer/sources" 2>/dev/null); do
  i=$((i + 1))
  [ "$i" -lt 60 ] || { echo 'FAIL: explorer did not start' >&2; exit 1; }
  sleep 2
done
version=$(printf '%s\n' "$sources" | jq -er '.sources[] | select(.id == "local") | .version')
explorer_api --get "$explorer_url/v1/explorer/sources/local/tree" \
  --data-urlencode "sourceVersion=$version" --data-urlencode 'path=.' |
  jq -e 'any(.directory.entries[]; .path == "README.md" and .kind == "file")' >/dev/null
file=$(explorer_api --get "$explorer_url/v1/explorer/sources/local/file" \
  --data-urlencode "sourceVersion=$version" --data-urlencode 'path=README.md')
printf '%s\n' "$file" | jq -e '.file.location.source == "local" and (.file.location.version | length == 64)' >/dev/null
content_version=$(printf '%s\n' "$file" | jq -r '.file.location.version')
explorer_api --get "$explorer_url/v1/explorer/sources/local/file" \
  --data-urlencode "sourceVersion=$version" --data-urlencode 'path=README.md' \
  --data-urlencode "expectedVersion=$content_version" >/dev/null
jq -n --arg version "$version" '{sourceVersion:$version,text:"forge-explorer",limit:10}' |
  explorer_api "$explorer_url/v1/explorer/sources/local/search" -H 'Content-Type: application/json' -d @- |
  jq -e '(.results.matches | length > 0) and all(.results.matches[]; .location.source == "local" and (.location.version | length == 64))' >/dev/null
[ "$(curl -sS -o /dev/null -w '%{http_code}' "$explorer_url/v1/explorer/sources")" = 401 ]
curl -fsS "$explorer_url/" | grep -q 'Forge Explorer'
curl -fsS "$explorer_url/app.js" >/dev/null
echo 'ok: explorer standalone app, configured root, versioned reads and search'
