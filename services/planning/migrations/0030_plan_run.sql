-- #479: an asynchronous plan run (POST /plans/{planCode}:run with
-- Prefer: respond-async). The run executes outside the request, so a 50k-item
-- plan is not cut off by the router's 30 s request timeout; plan_run is its
-- status record, read by GET /plan-runs/{id} from any replica.
--
-- At most one run of a plan is running (plan_run_one_running). The replica
-- executing it renews heartbeat_at every few seconds and reads
-- cancel_requested, so a cancel reaches it from any replica; a running row
-- whose heartbeat stopped (the process died) is reported failed and is
-- closed by the next run of the plan. legal_entity_id is the plan's owner, as
-- on plan_version, so a caller outside that entity cannot see the run.
CREATE TABLE IF NOT EXISTS plan_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  plan_code text NOT NULL CHECK (plan_code ~ '^[A-Z0-9][A-Z0-9_-]{1,19}$'),
  legal_entity_id uuid,
  status text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'cancelled')),
  phase text NOT NULL DEFAULT 'planning' CHECK (phase IN ('planning', 'persisting', 'done')),
  items integer NOT NULL CHECK (items >= 0),
  rows_written bigint NOT NULL DEFAULT 0 CHECK (rows_written >= 0),
  rows_total bigint NOT NULL DEFAULT 0 CHECK (rows_total >= 0),
  cancel_requested boolean NOT NULL DEFAULT false,
  idempotency_key text NOT NULL,
  input_hash text NOT NULL,
  requested_by text NOT NULL DEFAULT '',
  version_id uuid,
  replayed boolean NOT NULL DEFAULT false,
  problem jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  heartbeat_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT plan_run_idempotency_key UNIQUE (tenant_id, idempotency_key),
  CHECK ((status = 'running') = (finished_at IS NULL)),
  CHECK (status <> 'succeeded' OR version_id IS NOT NULL),
  CHECK (status <> 'failed' OR problem IS NOT NULL)
);
CREATE UNIQUE INDEX IF NOT EXISTS plan_run_one_running ON plan_run (tenant_id, plan_code) WHERE status = 'running';
ALTER TABLE plan_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE plan_run FORCE ROW LEVEL SECURITY;
CREATE POLICY plan_run_tenant_access ON plan_run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY plan_run_legal_entity_scope ON plan_run AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON plan_run TO passage_app';
  END IF;
END
$$;
