-- Wave 27 slice 14: sales agreement lifecycle (FR-27.10, BR-27.9).
-- A commitment can be revised (quantity, price, validity, tolerance) and
-- closed early, and the quantity of a released order that is cancelled goes
-- back to the commitment.
-- sales_agreement_version: one row per revision or close, with the field-level
-- diff, appended in the transaction that changes the agreement.
-- agreement_return: the quantity a cancelled order gave back to its release.
-- agreement_release stays append-only; a release is returned when a row here
-- names it, and at most one row can (release_id is unique).

ALTER TABLE sales_agreement DROP CONSTRAINT IF EXISTS sales_agreement_status_check;
ALTER TABLE sales_agreement ADD CONSTRAINT sales_agreement_status_check CHECK (status IN ('ACTIVE', 'EXHAUSTED', 'CLOSED'));
ALTER TABLE sales_agreement ADD COLUMN IF NOT EXISTS close_reason text;
ALTER TABLE sales_agreement ADD COLUMN IF NOT EXISTS closed_by text;
ALTER TABLE sales_agreement ADD COLUMN IF NOT EXISTS closed_at timestamptz;

CREATE TABLE IF NOT EXISTS sales_agreement_version (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  agreement_id uuid NOT NULL,
  version int NOT NULL CHECK (version > 1),
  kind text NOT NULL CHECK (kind IN ('REVISED', 'CLOSED', 'RETURNED')),
  actor text NOT NULL,
  reason text NOT NULL DEFAULT '',
  diff jsonb NOT NULL DEFAULT '[]'::jsonb,
  changed_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, agreement_id, version),
  FOREIGN KEY (tenant_id, agreement_id) REFERENCES sales_agreement (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS agreement_return (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  agreement_id uuid NOT NULL,
  release_id uuid NOT NULL,
  sales_order_id uuid NOT NULL,
  quantity numeric(20,6) NOT NULL CHECK (quantity > 0),
  actor text NOT NULL,
  returned_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, release_id),
  FOREIGN KEY (tenant_id, agreement_id) REFERENCES sales_agreement (tenant_id, id),
  FOREIGN KEY (tenant_id, release_id) REFERENCES agreement_release (tenant_id, id),
  FOREIGN KEY (tenant_id, sales_order_id) REFERENCES sales_order (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS agreement_return_agreement ON agreement_return (tenant_id, agreement_id);

ALTER TABLE sales_agreement_version ENABLE ROW LEVEL SECURITY;
ALTER TABLE sales_agreement_version FORCE ROW LEVEL SECURITY;
CREATE POLICY sales_agreement_version_tenant_access ON sales_agreement_version AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY sales_agreement_version_legal_entity_scope ON sales_agreement_version AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE agreement_return ENABLE ROW LEVEL SECURITY;
ALTER TABLE agreement_return FORCE ROW LEVEL SECURITY;
CREATE POLICY agreement_return_tenant_access ON agreement_return AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY agreement_return_legal_entity_scope ON agreement_return AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Both tables are append-only: the store only inserts and reads them.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON sales_agreement_version, agreement_return TO passage_app';
  END IF;
END
$$;
