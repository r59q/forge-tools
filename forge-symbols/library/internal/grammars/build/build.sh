#!/bin/sh
# Rebuilds the bundled grammar modules: ../<language>.wasm.
#
# Each module links the Tree-sitter runtime, one grammar and shim.c. Sources
# are pinned by version and SHA-256. Needs curl, tar and sha256sum. Set
# WASI_SDK_PATH to a wasi-sdk of the pinned version; on x86_64 Linux the
# script downloads it when unset.
set -eu

WASI_SDK_VERSION=34
WASI_SDK_SHA256=b761e3a0721dbae9c09a0059e5fdb2bf917d1b4a8a7b430fb3b5aafb0984b2c4

RUNTIME_VERSION=v0.27.2
RUNTIME_SHA256=356528363bf7fbad12a2cfeb5a2678c0118ef886a4d69ad9a379db5fdef0735f

here=$(cd "$(dirname "$0")" && pwd)
work=${SYMBOLS_BUILD_DIR:-$(mktemp -d)}
mkdir -p "$work"

# fetch <name> <url> <sha256> downloads and unpacks an archive into $work/<name>.
fetch() {
  if [ -d "$work/$1" ]; then return; fi
  curl --fail --silent --show-error --location --output "$work/$1.tar.gz" "$2"
  echo "$3  $work/$1.tar.gz" | sha256sum --check --quiet
  mkdir "$work/$1"
  tar -xzf "$work/$1.tar.gz" -C "$work/$1" --strip-components=1
}

# grammar <language> <repository> <version> <sha256> [source...] builds one module.
# Sources are relative to the grammar repository and default to src/parser.c.
# The directory of the first source holds the grammar headers.
grammar() {
  language=$1 repository=$2 version=$3 sha256=$4
  shift 4
  if [ $# -eq 0 ]; then set -- src/parser.c; fi

  archive=$(basename "$repository")-$version
  fetch "$archive" "https://github.com/$repository/archive/refs/tags/$version.tar.gz" "$sha256"
  headers=$work/$archive/$(dirname "$1")

  sources=
  for source in "$@"; do sources="$sources $work/$archive/$source"; done

  # shellcheck disable=SC2086 # sources is a list.
  "$WASI_SDK_PATH/bin/clang" --target=wasm32-wasip1 -O2 -DNDEBUG \
    -mexec-model=reactor -Wl,--strip-all -Wl,-z,stack-size=1048576 \
    -DSYM_LANGUAGE="tree_sitter_$language" \
    -I "$work/runtime/lib/include" -I "$work/runtime/lib/src" \
    -I "$headers" \
    "$work/runtime/lib/src/lib.c" $sources "$here/shim.c" \
    -o "$here/../$language.wasm"
  chmod 644 "$here/../$language.wasm"
  echo "built $language.wasm"
}

if [ -z "${WASI_SDK_PATH:-}" ]; then
  if [ "$(uname -sm)" != "Linux x86_64" ]; then
    echo "set WASI_SDK_PATH to wasi-sdk $WASI_SDK_VERSION" >&2
    exit 1
  fi
  fetch wasi-sdk "https://github.com/WebAssembly/wasi-sdk/releases/download/wasi-sdk-$WASI_SDK_VERSION/wasi-sdk-$WASI_SDK_VERSION.0-x86_64-linux.tar.gz" "$WASI_SDK_SHA256"
  WASI_SDK_PATH=$work/wasi-sdk
fi

fetch runtime "https://github.com/tree-sitter/tree-sitter/archive/refs/tags/$RUNTIME_VERSION.tar.gz" "$RUNTIME_SHA256"

# One line per language. Add external scanners after the checksum, e.g. src/parser.c src/scanner.c.
grammar go tree-sitter/tree-sitter-go v0.25.0 2dc241b97872c53195e01b86542b411a3c1a6201d9c946c78d5c60c063bba1ef
grammar java tree-sitter/tree-sitter-java v0.23.5 cb199e0faae4b2c08425f88cbb51c1a9319612e7b96315a174a624db9bf3d9f0
grammar javascript tree-sitter/tree-sitter-javascript v0.25.0 9712fc283d3dc01d996d20b6392143445d05867a7aad76fdd723824468428b86 src/parser.c src/scanner.c
grammar typescript tree-sitter/tree-sitter-typescript v0.23.2 2c4ce711ae8d1218a3b2f899189298159d672870b5b34dff5d937bed2f3e8983 typescript/src/parser.c typescript/src/scanner.c
grammar tsx tree-sitter/tree-sitter-typescript v0.23.2 2c4ce711ae8d1218a3b2f899189298159d672870b5b34dff5d937bed2f3e8983 tsx/src/parser.c tsx/src/scanner.c
