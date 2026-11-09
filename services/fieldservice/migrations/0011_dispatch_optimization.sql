-- Field service, PSG-45: dispatch optimization requested from the dispatch service
-- (BR-45.3). The row keeps the problem snapshot that was sent, the plan that came back
-- and, once a dispatcher accepts, which assignments were written. A failed call backs
-- off (60 s doubling to an hour) and the run is parked after five attempts.
CREATE TABLE IF NOT EXISTS service_dispatch_optimization (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  territory_id uuid NOT NULL,
  run_date date NOT NULL,
  time_zone text NOT NULL,
  status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'RUNNING', 'SUCCEEDED', 'ACCEPTED', 'PARKED')),
  request jsonb NOT NULL,
  skipped jsonb NOT NULL DEFAULT '[]',
  remote_run_id text,
  result jsonb,
  outcome jsonb,
  attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  last_error text,
  requested_by text NOT NULL DEFAULT '',
  accepted_by text,
  accepted_at timestamptz,
  idempotency_key text,
  request_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (status NOT IN ('SUCCEEDED', 'ACCEPTED') OR result IS NOT NULL),
  CHECK (status <> 'ACCEPTED' OR (outcome IS NOT NULL AND accepted_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS service_dispatch_optimization_territory ON service_dispatch_optimization (tenant_id, territory_id, run_date);
CREATE INDEX IF NOT EXISTS service_dispatch_optimization_recent ON service_dispatch_optimization (tenant_id, created_at DESC, id DESC);
ALTER TABLE service_dispatch_optimization ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_dispatch_optimization FORCE ROW LEVEL SECURITY;
CREATE POLICY service_dispatch_optimization_tenant_access ON service_dispatch_optimization AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY service_dispatch_optimization_legal_entity_scope ON service_dispatch_optimization AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON service_dispatch_optimization TO passage_app';
  END IF;
END
$$;
