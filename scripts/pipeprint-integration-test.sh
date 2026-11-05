#!/bin/sh
# Smoke test of the pipeprint service: convert, check, graph and render a
# workflow. FORGE_PIPEPRINT_TEST_TOKEN is supplied by the caller.
set -eu
: "${FORGE_PIPEPRINT_TEST_TOKEN:?set FORGE_PIPEPRINT_TEST_TOKEN}"
url=${FORGE_PIPEPRINT_TEST_URL:-http://127.0.0.1:8092}
api() { curl -fsS -H "Authorization: Bearer $FORGE_PIPEPRINT_TEST_TOKEN" "$@"; }
fail() {
  echo "FAIL: $*" >&2
  exit 1
}
i=0
until api -o /dev/null "$url/v1/pipeprint/formats" 2>/dev/null; do
  i=$((i + 1))
  [ "$i" -lt 60 ] || fail "pipeprint did not start"
  sleep 2
done
workflow='name: CI
on: {push: {branches: [main]}}
jobs:
  build: {runs-on: ubuntu-latest, steps: [{uses: actions/checkout@v4}, {run: make}]}
  test: {needs: build, runs-on: ubuntu-latest, steps: [{run: make test}]}'
request() { jq -n --arg c "$workflow" "{content: \$c} + $1"; }

gitlab=$(request '{to: "gitlab"}' | api "$url/v1/pipeprint/convert" -d @-) || fail "convert"
printf '%s\n' "$gitlab" | jq -e '(.content | contains("- make test")) and (.warnings | length > 0)' >/dev/null ||
  fail "convert to gitlab"
back=$(printf '%s\n' "$gitlab" | jq '{content: .content, from: "gitlab", to: "github"}' |
  api "$url/v1/pipeprint/convert" -d @-) || fail "convert back"
printf '%s\n' "$back" | jq -e '.content | contains("needs: build")' >/dev/null || fail "convert back to github"
echo "ok: pipeprint converts between github and gitlab"

request '{}' | api "$url/v1/pipeprint/check" -d @- |
  jq -e '.valid and .format == "github"' >/dev/null || fail "check"
request '{}' | api "$url/v1/pipeprint/graph" -d @- |
  jq -e '.levels == 2 and .edges[0].from == "build" and .edges[0].to == "test"' >/dev/null || fail "graph"
request '{ascii: true}' | api "$url/v1/pipeprint/render" -d @- |
  jq -e '.text | contains("<- build")' >/dev/null || fail "render"
[ "$(curl -sS -o /dev/null -w '%{http_code}' "$url/v1/pipeprint/formats")" = 401 ] || fail "unauthenticated request"
echo "ok: pipeprint check, graph, render and authentication"
