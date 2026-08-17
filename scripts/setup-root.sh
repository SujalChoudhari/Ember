#!/bin/sh
set -eu
ROOT=${1:-/var/lib/ember/blob}
if [ "$(id -u)" -ne 0 ]; then
  echo 'run as root to create the private provider root' >&2
  exit 1
fi
install -d -o ember -g ember -m 0750 "$ROOT"
install -d -o ember -g ember -m 0750 "$ROOT/staging" "$ROOT/objects" "$ROOT/quarantine"
printf '%s\n' "created private Ember Blob root at $ROOT (owner ember:ember, mode 0750)"
