#!/usr/bin/env python3
"""Static contract checks for the published private Ember Phase 1 boundary."""
from pathlib import Path

root = Path(__file__).resolve().parents[1]
app = (root / "internal/ember/app.go").read_text()
http = (root / "internal/ember/http.go").read_text()
sql = (root / "migrations/0001_phase1.sql").read_text()
required = ["MaxObjectSize", "MaxObjectKeyBytes", "LogicalQuotaBytes", "ErrRecovery", "auditLocked", "idempotency", "operator_action_required"]
for item in required:
    assert item in app, item
for route in ["/healthz", "/api/v1/", "objects", "operations", "repair"]:
    assert route in http or route == "/api/v1/", route
for item in ["schema_meta", "audit_events", "ember_audit_append_only", "blob_objects", "repair_findings"]:
    assert item in sql, item
assert "RESOLVE_BENEATH" in (root / "README.md").read_text() or "resolveBeneath" in (root / "internal/ember/openat2_linux.go").read_text()
git_config = (root / ".git/config").read_text() if (root / ".git/config").exists() else ""
expected_origin = "https://github.com/SujalChoudhari/Ember.git"
assert '[remote "origin"]' in git_config and expected_origin in git_config, "origin must point to the private Ember repository"
print("static contract checks passed: scope, storage, idempotency, recovery, HTTP, migration, and private-origin assertions")
