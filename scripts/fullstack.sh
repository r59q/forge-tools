#!/bin/sh
# Runs the full-stack example in examples/fullstack/.
#
# Usage: scripts/fullstack.sh start|stop|clear
#   start  create missing secrets, then build and start every service
#   stop   stop the services and keep their data
#   clear  stop the services and delete their data, such as stored forge
#          tokens and checkout files (including uncommitted work). .env is kept.
#
# On start, these optional variables add forge connections, if missing:
#   GITHUB_TOKEN      adds connection "github" for github.com
#   FORGEJO_TOKEN     adds connection "forgejo" for FORGEJO_HOSTNAME
#   FORGEJO_HOSTNAME  Forgejo host, such as git.example.com; default codeberg.org
# Adding connections needs curl and jq.
set -eu
cd "$(dirname "$0")/../examples/fullstack"
url=http://127.0.0.1:8083

api() {
  token=$(sed -n 's/^FORGE_MANAGEMENT_TOKEN=//p' .env)
  curl -fsS -H "Authorization: Bearer $token" "$@"
}

# Management has no health route; wait until it answers.
wait_for_management() {
  i=0
  until api -o /dev/null "$url/v1/connections" 2>/dev/null; do
    i=$((i + 1))
    if [ "$i" -ge 60 ]; then
      echo "management did not start" >&2
      exit 1
    fi
    sleep 2
  done
}

has_connection() {
  api "$url/v1/connections" | jq -e --arg n "$1" 'any(.connections[]?; .name == $n)' >/dev/null
}

# add_connection name forge host credential
# The credential goes through stdin to keep it out of process arguments.
add_connection() {
  if has_connection "$1"; then
    echo "Connection $1 exists"
    return
  fi
  if post_connection "$@"; then
    echo "Connection $1 added"
  else
    echo "Connection $1 failed: check its token and host (${3:-default})" >&2
  fi
}

post_connection() {
  jq -n --arg n "$1" --arg f "$2" --arg h "$3" --arg c "$4" \
    '{name: $n, forge: $f, host: $h, credential: $c}' |
    api "$url/v1/connections" -d @- >/dev/null
}

# forgejo_host prints FORGEJO_HOSTNAME without a URL scheme.
forgejo_host() {
  host=${FORGEJO_HOSTNAME:-codeberg.org}
  host=${host#https://}
  echo "${host%/}"
}

connect_forges() {
  github=${GITHUB_TOKEN:-}
  forgejo=${FORGEJO_TOKEN:-}
  if [ -z "$github$forgejo" ]; then
    return
  fi
  wait_for_management
  if [ -n "$github" ]; then
    add_connection github github "" "$github"
  fi
  if [ -n "$forgejo" ]; then
    add_connection forgejo forgejo "$(forgejo_host)" "$forgejo"
  fi
}

case "${1:-}" in
start | stop | clear)
  # Compose needs every secret set, even to stop.
  ./init-env.sh >/dev/null
  ;;
esac
case "${1:-}" in
start)
  docker compose up --build -d
  connect_forges
  echo "Management: http://127.0.0.1:8083/ (operator / FORGE_MANAGEMENT_TOKEN in examples/fullstack/.env)"
  ;;
stop)
  docker compose down
  ;;
clear)
  docker compose down -v
  ;;
*)
  echo "Usage: $0 start|stop|clear" >&2
  exit 2
  ;;
esac
