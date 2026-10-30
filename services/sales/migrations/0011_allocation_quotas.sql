-- Wave 27 slice 7: product allocation quotas (FR-27.16, BR-27.15, SAP OVZJ).
-- A quota caps the quantity of one product that may be confirmed for one
-- customer (or, customer NULL, every customer of the legal entity) in one
-- period. Confirmation consumes quota in requested-period order; cancelling
-- the order returns what each line consumed to the period it came from.

CREATE TABLE IF NOT EXISTS allocation_quota (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  product_id uuid NOT NULL,
  customer_id uuid,
  period_start date NOT NULL,
  period_end date NOT NULL,
  quota_qty numeric(20,6) NOT NULL CHECK (quota_qty > 0),
  consumed_qty numeric(20,6) NOT NULL DEFAULT 0 CHECK (consumed_qty >= 0),
  version int NOT NULL DEFAULT 1,
  idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 255),
  request_hash text NOT NULL,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (period_end >= period_start),
  CHECK (consumed_qty <= quota_qty)
);
-- One quota per (product, customer or all customers, period start).
CREATE UNIQUE INDEX IF NOT EXISTS allocation_quota_unique ON allocation_quota
  (tenant_id, legal_entity_id, product_id, coalesce(customer_id, '00000000-0000-0000-0000-000000000000'::uuid), period_start);
CREATE INDEX IF NOT EXISTS allocation_quota_lookup ON allocation_quota (tenant_id, legal_entity_id, product_id, period_start);

ALTER TABLE allocation_quota ENABLE ROW LEVEL SECURITY;
ALTER TABLE allocation_quota FORCE ROW LEVEL SECURITY;
CREATE POLICY allocation_quota_tenant_access ON allocation_quota AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY allocation_quota_legal_entity_scope ON allocation_quota AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- One row per quantity a line consumed from a quota. returned_at is set when
-- the order or the line is cancelled, or the line is changed and consumes again.
CREATE TABLE IF NOT EXISTS allocation_consumption (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  quota_id uuid NOT NULL,
  order_id uuid NOT NULL,
  line_id uuid NOT NULL,
  line_no int NOT NULL,
  quantity numeric(20,6) NOT NULL CHECK (quantity > 0),
  consumed_at timestamptz NOT NULL DEFAULT now(),
  returned_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, quota_id) REFERENCES allocation_quota (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS allocation_consumption_quota ON allocation_consumption (tenant_id, quota_id, consumed_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS allocation_consumption_active_line ON allocation_consumption (tenant_id, line_id) WHERE returned_at IS NULL;
CREATE INDEX IF NOT EXISTS allocation_consumption_order ON allocation_consumption (tenant_id, order_id);

ALTER TABLE allocation_consumption ENABLE ROW LEVEL SECURITY;
ALTER TABLE allocation_consumption FORCE ROW LEVEL SECURITY;
CREATE POLICY allocation_consumption_tenant_access ON allocation_consumption AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY allocation_consumption_legal_entity_scope ON allocation_consumption AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Grants for this slice's tables. The store updates both (consumed quantity,
-- quota changes, returned_at) and never deletes.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON allocation_quota, allocation_consumption TO passage_app';
  END IF;
END
$$;
