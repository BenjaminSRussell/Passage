-- Wave 27 slice 13: change approval per customer (FR-27.9, PRD 5.27.13
-- change_approval_required, PRD 5.27.6 SalesOrderApprove).
-- customer_change_policy: one row per (legal entity, customer) that has set the
-- switch; a customer without a row does not need approval (the PRD default).
-- order_change_request: a PATCH of a CONFIRMED order for such a customer, made
-- by a caller without SalesOrderApprove, is stored here instead of applied. The
-- change is the PATCH body, the diff is what it would change, and base_version
-- is the order version it was made against. Approval applies it in one
-- transaction with the decision; at most one request per order is PENDING.

CREATE TABLE IF NOT EXISTS customer_change_policy (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  approval_required boolean NOT NULL DEFAULT false,
  version int NOT NULL DEFAULT 1,
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, customer_id)
);

CREATE TABLE IF NOT EXISTS order_change_request (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  id uuid NOT NULL,
  order_id uuid NOT NULL,
  status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'APPROVED', 'REJECTED')),
  base_version int NOT NULL CHECK (base_version > 0),
  reason text NOT NULL DEFAULT '',
  change jsonb NOT NULL,
  diff jsonb NOT NULL DEFAULT '[]'::jsonb,
  requested_by text NOT NULL,
  requested_at timestamptz NOT NULL,
  decided_by text,
  decided_at timestamptz,
  decision_comment text NOT NULL DEFAULT '',
  applied_version int,
  version int NOT NULL DEFAULT 1,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, order_id) REFERENCES sales_order (tenant_id, id)
);

CREATE UNIQUE INDEX IF NOT EXISTS order_change_request_one_pending
  ON order_change_request (tenant_id, order_id) WHERE status = 'PENDING';
CREATE INDEX IF NOT EXISTS order_change_request_queue
  ON order_change_request (tenant_id, status, requested_at);

ALTER TABLE customer_change_policy ENABLE ROW LEVEL SECURITY;
ALTER TABLE customer_change_policy FORCE ROW LEVEL SECURITY;
CREATE POLICY customer_change_policy_tenant_access ON customer_change_policy AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY customer_change_policy_legal_entity_scope ON customer_change_policy AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE order_change_request ENABLE ROW LEVEL SECURITY;
ALTER TABLE order_change_request FORCE ROW LEVEL SECURITY;
CREATE POLICY order_change_request_tenant_access ON order_change_request AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY order_change_request_legal_entity_scope ON order_change_request AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Both stores insert and update by version; neither deletes.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON customer_change_policy, order_change_request TO passage_app';
  END IF;
END
$$;
