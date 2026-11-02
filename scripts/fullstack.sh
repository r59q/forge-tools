#!/bin/sh
# Runs the full-stack example in examples/fullstack/.
#
# Usage: scripts/fullstack.sh start|stop|clear
#   start  create missing secrets, then build and start every service
#   stop   stop the services and keep their data
#   clear  stop the services and delete their data, such as stored forge
#          tokens and workspace files (including uncommitted work). .env is kept.
set -eu
cd "$(dirname "$0")/../examples/fullstack"
case "${1:-}" in
start | stop | clear)
  # Compose needs every secret set, even to stop.
  ./init-env.sh >/dev/null
  ;;
esac
case "${1:-}" in
start)
  docker compose up --build -d
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
