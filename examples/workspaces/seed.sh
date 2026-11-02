#!/bin/sh
# Seed a disposable local repository once, then start forge-workspaces.
set -eu
if [ ! -d /data/demo ]; then
  stage=$(mktemp -d /data/.demo-XXXXXX)
  trap 'rm -rf "$stage"' EXIT
  cp -R /example/demo/. "$stage/"
  git -C "$stage" init -q -b main
  git -C "$stage" add .
  git -C "$stage" -c user.name=Example -c user.email=example@example.com commit -qm 'Initial greeting'
  git -C "$stage" tag v1
  sed -i 's/Hello,/Welcome,/' "$stage/src/greeting.js"
  git -C "$stage" -c user.name=Example -c user.email=example@example.com commit -qam 'Welcome the reader'
  mv "$stage" /data/demo
  trap - EXIT
fi
exec forge-workspaces-service
