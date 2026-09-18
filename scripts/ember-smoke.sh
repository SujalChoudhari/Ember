#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
GOOS_VALUE=${GOOS:-$(go env GOOS)}
GOARCH_VALUE=${GOARCH:-$(go env GOARCH)}
BINARY=${1:-"$ROOT/dist/ember-${GOOS_VALUE}-${GOARCH_VALUE}"}

if [ ! -x "$BINARY" ]; then
  printf '%s\n' "executable not found: $BINARY; run 'make package' first" >&2
  exit 1
fi

STATE_DIR=$(mktemp -d "${TMPDIR:-/tmp}/ember-smoke.XXXXXX")
trap 'rm -rf "$STATE_DIR"' EXIT HUP INT TERM
umask 077

"$BINARY" --state-dir "$STATE_DIR" resource create --type group --name smoke >/dev/null
"$BINARY" --state-dir "$STATE_DIR" resource get --id resource-00000001 >/dev/null
"$BINARY" --state-dir "$STATE_DIR" resource create --scope resource-00000001 --type bucket --name objects --parent resource-00000001 >/dev/null
"$BINARY" --state-dir "$STATE_DIR" blob put --scope resource-00000001 --bucket resource-00000002 --key greeting --data hello-ember >/dev/null
"$BINARY" --state-dir "$STATE_DIR" blob get --scope resource-00000001 --bucket resource-00000002 --key greeting >/dev/null

"$BINARY" --state-dir "$STATE_DIR" tenant create --id alpha --name Alpha >/dev/null
"$BINARY" --state-dir "$STATE_DIR" tenant create --id beta --name Beta >/dev/null
"$BINARY" --state-dir "$STATE_DIR" resource create --tenant alpha --type group --name shared >/dev/null
"$BINARY" --state-dir "$STATE_DIR" resource create --tenant beta --type group --name shared >/dev/null
ALPHA_RESOURCES=$("$BINARY" --state-dir "$STATE_DIR" resource list --tenant alpha --limit 10)
BETA_RESOURCES=$("$BINARY" --state-dir "$STATE_DIR" resource list --tenant beta --limit 10)
printf '%s' "$ALPHA_RESOURCES" | grep -Fq 'resource-tenant-alpha-'
printf '%s' "$BETA_RESOURCES" | grep -Fq 'resource-tenant-beta-'
if printf '%s' "$ALPHA_RESOURCES" | grep -Fq 'resource-tenant-beta-'; then
  printf '%s\n' 'tenant resource isolation failed for alpha' >&2
  exit 1
fi
if printf '%s' "$BETA_RESOURCES" | grep -Fq 'resource-tenant-alpha-'; then
  printf '%s\n' 'tenant resource isolation failed for beta' >&2
  exit 1
fi

"$BINARY" --state-dir "$STATE_DIR" reset --confirm >/dev/null
for path in resources.json workloads.json operations.json audit.json apply-progress.json blobs/metadata.json blobs/objects tenants/alpha tenants/beta; do
  if [ -e "$STATE_DIR/$path" ]; then
    printf '%s\n' "reset left owned residue: $STATE_DIR/$path" >&2
    exit 1
  fi
done
if [ ! -f "$STATE_DIR/platform.db" ]; then
  printf '%s\n' 'reset removed the versioned platform.db boundary' >&2
  exit 1
fi

"$BINARY" --state-dir "$STATE_DIR" resource create --type group --name repeat >/dev/null
printf '%s\n' "ember clean-machine smoke passed"
