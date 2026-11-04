#!/bin/sh
# Starts the explorer dev stack, connects the forge account from .env through
# the connector, checks out FORGE_DEV_REPOSITORIES and serves them in the
# explorer. Safe to run again: it reuses the connection and checkouts.
# Run it with `npm run dev:setup`. Needs Docker Compose, curl, jq and openssl.
set -eu
cd "$(dirname "$0")/.."
umask 077

fail() {
  echo "$*" >&2
  exit 1
}

# value prints the last NAME=value line of .env, without quotes.
value() {
  sed -n "s/^$1=//p" .env | tail -n 1 | sed "s/^[\"']//; s/[\"']\$//"
}

set_value() {
  grep -v "^$1=" .env > .env.tmp || true
  echo "$1=$2" >> .env.tmp
  mv .env.tmp .env
}

add_secret() {
  [ -n "$(value "$1")" ] || set_value "$1" "$2"
}

create_env() {
  if [ ! -e .env ]; then
    cp .env.example .env
    fail "Created .env. Set FORGE_DEV_TOKEN and FORGE_DEV_REPOSITORIES in it, then run npm run dev:setup again."
  fi

  add_secret FORGE_BROKER_KEY "$(openssl rand -base64 32)"
  for name in FORGE_BROKER_ADMIN_TOKEN FORGE_MANAGEMENT_TOKEN FORGE_PROXY_TOKEN \
    FORGE_CHECKOUTS_CONNECTOR_TOKEN FORGE_CHECKOUTS_TOKEN FORGE_EXPLORER_TOKEN; do
    add_secret "$name" "$(openssl rand -hex 32)"
  done
}

management() { curl -fsS -H "Authorization: Bearer $(value FORGE_MANAGEMENT_TOKEN)" "$@"; }
checkouts() { curl -fsS -H "Authorization: Bearer $(value FORGE_CHECKOUTS_TOKEN)" "$@"; }

wait_for_management() {
  i=0
  until management -o /dev/null http://127.0.0.1:8183/v1/connections 2>/dev/null; do
    i=$((i + 1))
    [ "$i" -lt 60 ] || fail "management did not start; see: docker compose logs management"
    sleep 2
  done
}

# connect prints the ID of the explorer-dev connection, adding it or
# replacing its token. The forge token goes through the environment and
# stdin, never through process arguments.
connect() {
  url=http://127.0.0.1:8183/v1/connections
  id=$(management "$url" | jq -r '.connections[]? | select(.name == "explorer-dev") | .id' | head -n 1)

  if [ -n "$id" ]; then
    FORGE_DEV_TOKEN=$(value FORGE_DEV_TOKEN) jq -n '{credential: $ENV.FORGE_DEV_TOKEN}' |
      management "$url/$id/credential" -d @- >/dev/null || fail "the forge rejected FORGE_DEV_TOKEN"
    echo "$id"
    return
  fi

  FORGE_DEV_TOKEN=$(value FORGE_DEV_TOKEN) jq -n \
    --arg forge "$(value FORGE_DEV_FORGE)" --arg host "$(value FORGE_DEV_HOST)" \
    '{forge: $forge, host: $host, name: "explorer-dev", credential: $ENV.FORGE_DEV_TOKEN}' |
    management "$url" -d @- | jq -er .id || fail "could not add the connection; check FORGE_DEV_* in .env"
}

# checkout prints the location of a checkout of repository $2 from
# connection $1, creating it when missing.
checkout() {
  url=http://127.0.0.1:8188/v1/checkouts
  reference=$(value FORGE_DEV_REFERENCE)
  location=$(checkouts "$url" | jq -r --arg source "$1" --arg path "$2" --arg ref "$reference" \
    '.checkouts[]? | select(.repository.source == $source and .repository.path == $path
      and .reference == $ref and .state != "failed") | .location' | head -n 1)

  # Management may still be registering checkouts in the broker, so retry.
  i=0
  while [ -z "$location" ]; do
    if created=$(jq -n --arg source "$1" --arg path "$2" --arg ref "$reference" \
      '{repository: {source: $source, path: $path}, reference: $ref}' |
      checkouts "$url" -d @- 2>/dev/null); then
      location=$(echo "$created" | jq -r .location)
      break
    fi
    i=$((i + 1))
    [ "$i" -lt 30 ] || fail "could not check out $2; see: docker compose logs checkouts"
    sleep 2
  done

  echo "$location"
}

# root_id turns owner/name into an explorer source ID, such as owner-name.
root_id() {
  printf '%s' "$1" | tr -c 'A-Za-z0-9_-' '-' | cut -c 1-64
}

create_env
[ -n "$(value FORGE_DEV_TOKEN)" ] || fail "Set FORGE_DEV_TOKEN in .env."
[ -n "$(value FORGE_DEV_REPOSITORIES)" ] || fail "Set FORGE_DEV_REPOSITORIES in .env."

docker compose up --build -d
wait_for_management
connection=$(connect)

roots=""
for repository in $(value FORGE_DEV_REPOSITORIES); do
  echo "Checking out $repository"
  roots="$roots${roots:+;}$(root_id "$repository")=$(checkout "$connection" "$repository")"
done

set_value FORGE_EXPLORER_ROOTS "$roots"
docker compose up -d explorer

cat <<EOF

Explorer service: http://127.0.0.1:8190 (token: FORGE_EXPLORER_TOKEN in .env)
Management:       http://127.0.0.1:8183 (operator / FORGE_MANAGEMENT_TOKEN in .env)

For live reload, run npm run dev and open http://127.0.0.1:8091
EOF
