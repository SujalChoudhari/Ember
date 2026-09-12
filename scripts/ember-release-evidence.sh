#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
ARTIFACT_DIR=$(mktemp -d "${TMPDIR:-/tmp}/ember-release-evidence.XXXXXX")
BUILD_DIR="$ARTIFACT_DIR/build"
trap 'rm -rf "$ARTIFACT_DIR"' EXIT HUP INT TERM

mkdir -p "$BUILD_DIR"

go test ./...
go test -race ./...
go vet ./...
go build -buildvcs=false -trimpath -o "$BUILD_DIR/ember" ./cmd/ember

make OUT_DIR="$ARTIFACT_DIR/package" package
GOOS_VALUE=${GOOS:-$(go env GOOS)}
GOARCH_VALUE=${GOARCH:-$(go env GOARCH)}
BINARY="$ARTIFACT_DIR/package/ember-${GOOS_VALUE}-${GOARCH_VALUE}"
sha256sum --check "$BINARY.sha256"
"$ROOT/scripts/ember-smoke.sh" "$BINARY"

printf '%s\n' "ember release evidence passed"