-- Persisted approval requests (FR-47.8, BR-47.3). A run that proposes a gated action opens one request;
-- an approver's decision is recorded first-writer-wins (UPDATE ... WHERE decided_at IS NULL), so two
-- approvers can never both decide it. Tenant row-level security as in 0001.

CREATE TABLE IF NOT EXISTS approval_request (
  tenant_id uuid NOT NULL,
  action_id uuid NOT NULL,
  run_id text NOT NULL,
  seq integer NOT NULL CHECK (seq >= 1),
  tool_name text NOT NULL,
  invoker_user_id text NOT NULL,
  approval_duty text NOT NULL,
  amount text NOT NULL CHECK (amount ~ '^-?[0-9]+(\.[0-9]+)?$'),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  self_approval_limit text NOT NULL CHECK (self_approval_limit ~ '^-?[0-9]+(\.[0-9]+)?$'),
  self_approval_currency text NOT NULL CHECK (self_approval_currency ~ '^[A-Z]{3}$'),
  expiry_hours integer CHECK (expiry_hours IS NULL OR expiry_hours BETWEEN 1 AND 240),
  proposal_json text NOT NULL,
  requested_at timestamptz NOT NULL DEFAULT now(),
  decision text CHECK (decision IN ('APPROVED', 'REJECTED')),
  reason text CHECK (reason IS NULL OR reason = 'EXPIRED'),
  code text CHECK (code IS NULL OR code = 'AGT-0208'),
  approver_user_id text,
  decided_at timestamptz,
  PRIMARY KEY (tenant_id, action_id),
  UNIQUE (tenant_id, run_id, seq),
  FOREIGN KEY (tenant_id, run_id) REFERENCES agent_run (tenant_id, run_id),
  CHECK ((decision IS NULL) = (decided_at IS NULL) AND (decision IS NULL) = (approver_user_id IS NULL))
);
CREATE INDEX IF NOT EXISTS approval_request_pending ON approval_request (tenant_id, requested_at) WHERE decision IS NULL;
ALTER TABLE approval_request ENABLE ROW LEVEL SECURITY;
ALTER TABLE approval_request FORCE ROW LEVEL SECURITY;
CREATE POLICY approval_request_tenant_access ON approval_request AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON approval_request TO passage_app';
  END IF;
END
$$;
