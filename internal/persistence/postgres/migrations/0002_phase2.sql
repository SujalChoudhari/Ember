CREATE TABLE IF NOT EXISTS declarative_states (
  logical_id text NOT NULL,
  resource_id text PRIMARY KEY REFERENCES resources(id) ON DELETE CASCADE,
  api_version text NOT NULL,
  type text NOT NULL,
  scope text NOT NULL,
  parent_id text,
  spec_hash text NOT NULL,
  spec_json jsonb NOT NULL,
  lifecycle jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(scope, logical_id)
);
CREATE INDEX IF NOT EXISTS declarative_states_scope_idx ON declarative_states(scope);
