#!/bin/sh
# Deploys the test environment over SSH, then checks it through its URL.
#
# Usage: DEPLOY_SSH=user@host DEPLOY_URL=https://test.example.com deploy/test/deploy.sh
#   DEPLOY_SSH  SSH destination of the server
#   DEPLOY_URL  public URL of the TLS proxy in front of nginx
#   DEPLOY_DIR  folder on the server, relative to home; default services/forgetools
#
# These optional tokens add forge connections, if missing:
#   TEST_GITHUB_TOKEN        adds connection "github" for github.com
#   TEST_GITLAB_TOKEN        adds connection "gitlab" for gitlab.com
#   TEST_FORGEJO_TOKEN       adds connection "forgejo" for TEST_FORGEJO_HOST
#   TEST_FORGEJO_HOST        Forgejo host, such as git.example.com
#   TEST_FORGEJO_REPOSITORY  Forgejo repository the reactor polls, if set
#
# Needs ssh, tar, curl and jq. The server needs Docker Compose and openssl.
set -eu
cd "$(dirname "$0")"
: "${DEPLOY_SSH:?set DEPLOY_SSH}"
: "${DEPLOY_URL:?set DEPLOY_URL}"
dir=${DEPLOY_DIR:-services/forgetools}
url=${DEPLOY_URL%/}

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

remote() {
  ssh "$DEPLOY_SSH" "cd $dir && $1"
}

# mask hides a value in GitHub Actions logs.
mask() {
  if [ -n "${GITHUB_ACTIONS:-}" ]; then
    echo "::add-mask::$1"
  fi
}

# secret prints a value from the server's .env.
secret() {
  remote "sed -n 's/^$1=//p' .env"
}

wait_for() {
  i=0
  until "$@" >/dev/null 2>&1; do
    i=$((i + 1))
    [ "$i" -lt 60 ] || return 1
    sleep 2
  done
}

upload() {
  ssh "$DEPLOY_SSH" "mkdir -p $dir"
  tar -cf - compose.yaml nginx.conf init-env.sh | remote "tar -xf -"
}

start() {
  remote "./init-env.sh && docker compose pull --quiet &&
    docker compose up -d --remove-orphans --wait --wait-timeout 300"
}

api() {
  curl -fsS -H "Authorization: Bearer $token" "$@"
}

has_connection() {
  api "$url/management/v1/connections" | jq -e --arg n "$1" 'any(.connections[]?; .name == $n)' >/dev/null
}

# add_connection name forge host credential
add_connection() {
  if [ -z "$4" ] || has_connection "$1"; then
    return
  fi
  if post_connection "$@"; then
    echo "Connection $1 added"
  else
    echo "Connection $1 failed: check its token and host" >&2
  fi
}

# The credential goes through stdin to keep it out of process arguments.
post_connection() {
  jq -n --arg n "$1" --arg f "$2" --arg h "$3" --arg c "$4" \
    '{name: $n, forge: $f, host: $h, credential: $c}' |
    api "$url/management/v1/connections" -d @- >/dev/null
}

connect_forges() {
  add_connection github github "" "${TEST_GITHUB_TOKEN:-}"
  add_connection gitlab gitlab "" "${TEST_GITLAB_TOKEN:-}"
  add_connection forgejo forgejo "${TEST_FORGEJO_HOST:-}" "${TEST_FORGEJO_TOKEN:-}"
}

# configure_reactor writes reactor.json and recreates the reactor on change.
configure_reactor() {
  api "$url/management/v1/connections" |
    jq --arg repository "${TEST_FORGEJO_REPOSITORY:-}" -f reactor.jq |
    remote "cat > reactor.json.new &&
      if cmp -s reactor.json.new reactor.json; then rm reactor.json.new; else
        mv reactor.json.new reactor.json &&
        docker compose up -d --force-recreate --wait --wait-timeout 120 reactor; fi"
}

# check_routes expects 401 without a token from each service behind nginx.
check_routes() {
  for route in repositories/v1/sources issues/v1/sources actions/v1/sources \
    checkouts/v1/checkouts code/v1/sources changes/v1/sources review/v1/reviews \
    pipeprint/v1/pipeprint/formats reactor/v1/subscriptions; do
    status=$(status "$url/$route")
    [ "$status" = 401 ] || fail "GET /$route answered $status, want 401"
  done
}

# status prints the HTTP status of a GET, or 000 and curl's error.
status() {
  curl -sS -o /dev/null -w '%{http_code}' "$1" 2>&1 || true
}

wait_for_nginx() {
  wait_for curl -fsS "$url/healthz" && return
  fail "GET $url/healthz answered $(status "$url/healthz"); a 502 means the TLS proxy cannot reach forgetools:6080"
}

upload
start
token=$(secret FORGE_MANAGEMENT_TOKEN)
mask "$token"
[ -n "$token" ] || fail "no FORGE_MANAGEMENT_TOKEN on the server"

wait_for_nginx
wait_for api "$url/management/v1/connections" || fail "management did not start"
connect_forges
configure_reactor
check_routes
echo "Deployed to $url"
