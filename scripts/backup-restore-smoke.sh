#!/bin/sh
set -eu
: "${EMBER_DATABASE_URL:?set EMBER_DATABASE_URL to a private local PostgreSQL URL}"
BACKUP=${1:-./var/ember-phase1.dump}
RESTORE_DB=${RESTORE_DB:-ember_restore_smoke}
mkdir -p "$(dirname "$BACKUP")"
pg_dump --format=custom --file="$BACKUP" "$EMBER_DATABASE_URL"
createdb "$RESTORE_DB"
trap 'dropdb --if-exists "$RESTORE_DB"' EXIT
pg_restore --exit-on-error --dbname="$RESTORE_DB" "$BACKUP"
printf '%s\n' "custom-format backup and disposable restore smoke test passed: $BACKUP"
