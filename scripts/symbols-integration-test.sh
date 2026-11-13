#!/bin/sh
# Smoke test of the symbols service: analyze two Go files where one function
# calls the other, then navigate the call both ways.
# FORGE_SYMBOLS_TEST_TOKEN is supplied by the caller.
set -eu
: "${FORGE_SYMBOLS_TEST_TOKEN:?set FORGE_SYMBOLS_TEST_TOKEN}"
url=${FORGE_SYMBOLS_TEST_URL:-http://127.0.0.1:8094}
api() { curl -fsS -H "Authorization: Bearer $FORGE_SYMBOLS_TEST_TOKEN" "$@"; }
fail() {
  echo "FAIL: $*" >&2
  exit 1
}
# The service has no health route; wait until an empty analysis answers.
i=0
until api -o /dev/null "$url/v1/symbols/analyze" -d '{"files":[]}' 2>/dev/null; do
  i=$((i + 1))
  [ "$i" -lt 60 ] || fail "symbols did not start"
  sleep 2
done
main='package main

func main() {
	Greet("world")
}
'
greet='package main

import "fmt"

func Greet(name string) {
	fmt.Println("hello", name)
}
'
request() {
  jq -n --arg m "$main" --arg g "$greet" \
    "{files: [{path: \"main.go\", content: (\$m | @base64)},
      {path: \"greet.go\", content: (\$g | @base64)}]} + $1"
}

request '{}' | api "$url/v1/symbols/analyze" -d @- |
  jq -e '[.analyses[] | .path + ":" + (.definitions[] | .kind + " " + .name)] ==
    ["main.go:function main", "greet.go:function Greet"]' >/dev/null || fail "analyze"
echo "ok: symbols analyzes definitions"

# The position is on the name of main.
request '{target: {path: "main.go", line: 3, column: 6}}' | api "$url/v1/symbols/outgoing" -d @- |
  jq -e '.name == "main" and (.groups | length) == 1 and
    .groups[0].definition.precision == "heuristic" and
    any(.groups[0].references[]; .reference.name == "Greet" and .reference.kind == "call" and
      .precision == "heuristic")' >/dev/null || fail "outgoing"
request '{target: {name: "Greet", path: "greet.go"}}' | api "$url/v1/symbols/incoming" -d @- |
  jq -e '.name == "Greet" and (.groups | length) == 1 and
    .groups[0].caller.path == "main.go" and .groups[0].caller.definition.name == "main" and
    .groups[0].caller.precision == "heuristic" and
    all(.groups[0].references[]; .precision == "heuristic")' >/dev/null || fail "incoming"
echo "ok: symbols navigates outgoing and incoming calls"

[ "$(curl -sS -o /dev/null -w '%{http_code}' "$url/v1/symbols/analyze" -d '{"files":[]}')" = 401 ] ||
  fail "unauthenticated request"
echo "ok: symbols authentication"
