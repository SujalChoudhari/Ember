CREATE TABLE IF NOT EXISTS schema_meta (
  component text PRIMARY KEY,
  version integer NOT NULL,
  compatible_min integer NOT NULL,
  compatible_max integer NOT NULL,
  applied_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO schema_meta(component, version, compatible_min, compatible_max)
VALUES ('phase1', 1, 1, 1)
ON CONFLICT (component) DO NOTHING;

CREATE TABLE IF NOT EXISTS resources (
  id text PRIMARY KEY,
  name text NOT NULL,
  type text NOT NULL,
  parent_id text REFERENCES resources(id),
  scope text NOT NULL,
  desired_state text NOT NULL,
  observed_state text NOT NULL,
  tags jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(parent_id, type, name)
);
CREATE TABLE IF NOT EXISTS operations (
  id text PRIMARY KEY,
  action text NOT NULL,
  status text NOT NULL CHECK (status IN ('accepted','running','succeeded','failed','cancelled','recovery_required')),
  resource_id text,
  scope text NOT NULL,
  request_id text NOT NULL,
  correlation_id text NOT NULL,
  error_code text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS idempotency_records (
  principal text NOT NULL,
  endpoint text NOT NULL,
  idem_key text NOT NULL,
  request_hash text NOT NULL,
  operation_id text NOT NULL REFERENCES operations(id),
  expires_at timestamptz NOT NULL,
  PRIMARY KEY(principal, endpoint, idem_key)
);
CREATE TABLE IF NOT EXISTS audit_events (
  id text PRIMARY KEY,
  principal text NOT NULL,
  action text NOT NULL,
  outcome text NOT NULL,
  target text,
  key_hash text,
  scope text NOT NULL,
  request_id text NOT NULL,
  correlation_id text NOT NULL,
  reason text,
  policy_version text NOT NULL,
  at timestamptz NOT NULL DEFAULT now()
);
CREATE OR REPLACE FUNCTION ember_audit_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'audit_events is append-only';
END;
$$;
DROP TRIGGER IF EXISTS audit_no_update ON audit_events;
CREATE TRIGGER audit_no_update BEFORE UPDATE OR DELETE ON audit_events FOR EACH ROW EXECUTE FUNCTION ember_audit_append_only();
REVOKE UPDATE, DELETE ON audit_events FROM PUBLIC;

CREATE TABLE IF NOT EXISTS locks (
  resource_id text NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
  kind text NOT NULL,
  note text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(resource_id, kind)
);
CREATE TABLE IF NOT EXISTS blob_objects (
  bucket_id text NOT NULL REFERENCES resources(id),
  object_key text NOT NULL,
  version_id text NOT NULL,
  sha256 text NOT NULL,
  etag text NOT NULL,
  size bigint NOT NULL CHECK (size >= 0 AND size <= 10485760),
  opaque_path text NOT NULL,
  committed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(bucket_id, object_key)
);
CREATE TABLE IF NOT EXISTS repair_findings (
  id text PRIMARY KEY,
  kind text NOT NULL,
  reference text NOT NULL,
  status text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
