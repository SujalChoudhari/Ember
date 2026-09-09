#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
OUT_DIR=${OUT_DIR:-"$ROOT/dist"}
GOOS_VALUE=${GOOS:-$(go env GOOS)}
GOARCH_VALUE=${GOARCH:-$(go env GOARCH)}
BINARY="$OUT_DIR/ember-${GOOS_VALUE}-${GOARCH_VALUE}"

mkdir -p "$OUT_DIR"
GOOS="$GOOS_VALUE" GOARCH="$GOARCH_VALUE" CGO_ENABLED=0 \
  go build -trimpath -o "$BINARY" ./cmd/ember
sha256sum "$BINARY" > "$BINARY.sha256"
printf '%s\n' "$BINARY"
