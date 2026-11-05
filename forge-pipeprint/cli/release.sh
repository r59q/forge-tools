#!/bin/sh
# Builds pipeprint release binaries and SHA256SUMS into dist/.
#
# Usage: VERSION=v0.1.0 ./release.sh
set -eu
cd "$(dirname "$0")"
: "${VERSION:?set VERSION, such as v0.1.0}"
case "$VERSION" in
v[0-9]*.[0-9]*.[0-9]*) ;;
*)
  echo "VERSION must look like v1.2.3" >&2
  exit 2
  ;;
esac
rm -rf dist
mkdir dist
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os=${target%/*}
  arch=${target#*/}
  ext=""
  [ "$os" = windows ] && ext=.exe
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
    -ldflags="-s -w -X main.version=$VERSION" \
    -o "dist/pipeprint-$VERSION-$os-$arch$ext" ./cmd/pipeprint
done
cd dist
sha256sum -- * > SHA256SUMS
