#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

required_docs='architecture.md cli-reference.md lifecycle-and-blobs.md deployment-and-recovery.md azure-shaped-compatibility.md sprint-2-release-evidence.md sprint-2-resource-blob-evidence.md sprint-2-queue-event-evidence.md'
for doc in $required_docs; do
  test -f "$ROOT/docs/$doc" || {
    printf '%s\n' "missing documentation file: docs/$doc" >&2
    exit 1
  }
done

grep -Fq 'docs/index.md' "$ROOT/README.md"
grep -Fq 'architecture.md' "$ROOT/docs/index.md"
grep -Fq 'cli-reference.md' "$ROOT/docs/index.md"
grep -Fq 'lifecycle-and-blobs.md' "$ROOT/docs/index.md"
grep -Fq 'deployment-and-recovery.md' "$ROOT/docs/index.md"
grep -Fq 'make smoke' "$ROOT/README.md"
grep -Fq './scripts/ember-release-evidence.sh' "$ROOT/docs/sprint-2-release-evidence.md"
grep -Fq -- '--state-dir' "$ROOT/docs/cli-reference.md"
grep -Fq -- '--confirm' "$ROOT/docs/cli-reference.md"
grep -Fq -- 'deployment recovery run' "$ROOT/docs/deployment-and-recovery.md"
printf '%s\n' 'ember documentation links and command references passed'
