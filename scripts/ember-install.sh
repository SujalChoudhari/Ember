#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
PREFIX=${PREFIX:-"$HOME/.local"}
GOOS_VALUE=${GOOS:-$(go env GOOS)}
GOARCH_VALUE=${GOARCH:-$(go env GOARCH)}
BINARY=${BINARY:-"$ROOT/dist/ember-${GOOS_VALUE}-${GOARCH_VALUE}"}
DEST="$PREFIX/bin/ember"

if [ ! -f "$BINARY" ]; then
  printf '%s\n' "package not found: $BINARY; run 'make package' first" >&2
  exit 1
fi
if [ -f "$BINARY.sha256" ]; then
  (cd "$(dirname -- "$BINARY")" && sha256sum -c "$(basename -- "$BINARY").sha256" >/dev/null)
fi
mkdir -p "$(dirname -- "$DEST")"
install -m 0755 "$BINARY" "$DEST"
printf '%s\n' "$DEST"
