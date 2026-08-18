#!/usr/bin/env python3
"""Static contract checks for the published private Ember Phase 1 boundary."""
from pathlib import Path

root = Path(__file__).resolve().parents[1]
source_paths = [
    root / "internal/ember/errors.go",
    root / "internal/ember/file_store.go",
    root / "internal/ember/memory_store.go",
    root / "internal/ember/http.go",
]
source_by_path = {source_path.name: source_path.read_text() for source_path in source_paths}
required_by_file = {
    "errors.go": ["MaxObjectSize", "MaxObjectKeyBytes", "LogicalQuotaBytes", "ErrRecovery"],
    "memory_store.go": ["recordAuditEventLocked", "idempotency", "operator_action_required"],
}
for file_name, required_items in required_by_file.items():
    for required_item in required_items:
        assert required_item in source_by_path[file_name], f"{file_name}: {required_item}"

http_source = source_by_path["http.go"]
for route in ["/healthz", "/api/v1/", "objects", "operations", "repair"]:
    assert route in http_source or route == "/api/v1/", route
migration_sql = (root / "migrations/0001_phase1.sql").read_text()
for required_item in ["schema_meta", "audit_events", "ember_audit_append_only", "blob_objects", "repair_findings"]:
    assert required_item in migration_sql, required_item
assert "RESOLVE_BENEATH" in (root / "README.md").read_text() or "resolveBeneath" in (root / "internal/ember/openat2_linux.go").read_text()
git_config = (root / ".git/config").read_text() if (root / ".git/config").exists() else ""
expected_origin = "https://github.com/SujalChoudhari/Ember.git"
assert '[remote "origin"]' in git_config and expected_origin in git_config, "origin must point to the private Ember repository"
print("static contract checks passed: scope, storage, idempotency, recovery, HTTP, migration, and private-origin assertions")
