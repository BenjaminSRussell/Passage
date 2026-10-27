-- Wave 21 slice 3: rework orders (FR-21.20) with their reference, the rework
-- variance and its posting-profile account, and as-built genealogy (FR-21.19).
-- Additive: existing rows keep their meaning (no reference, empty rework account).

ALTER TABLE production_order
  ADD COLUMN IF NOT EXISTS rework_reference_type text CHECK (rework_reference_type IN ('order','batch','serial')),
  ADD COLUMN IF NOT EXISTS rework_reference_id text;
-- A reference has both parts, and only a rework order carries one.
ALTER TABLE production_order DROP CONSTRAINT IF EXISTS production_order_rework_reference_check;
ALTER TABLE production_order ADD CONSTRAINT production_order_rework_reference_check CHECK (
  (rework_reference_type IS NULL) = (rework_reference_id IS NULL)
  AND (rework_reference_type IS NULL OR origin_type = 'Rework'));
CREATE INDEX IF NOT EXISTS production_order_rework_reference
  ON production_order (tenant_id, rework_reference_type, rework_reference_id) WHERE rework_reference_type IS NOT NULL;

ALTER TABLE production_posting_profile
  ADD COLUMN IF NOT EXISTS rework_variance_account text NOT NULL DEFAULT '';

ALTER TABLE production_variance DROP CONSTRAINT IF EXISTS production_variance_variance_type_check;
ALTER TABLE production_variance ADD CONSTRAINT production_variance_variance_type_check
  CHECK (variance_type IN ('lot-size','production-quantity','production-price','production-substitution','rework'));

-- AsBuiltRecord (PRD 4.21.4): one component serial or batch consumed into one
-- finished serial or batch of an order. A detached record stays for the audit
-- trail and no longer counts.
CREATE TABLE IF NOT EXISTS production_as_built (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  order_id uuid NOT NULL,
  finished_item_id uuid NOT NULL,
  finished_serial_or_batch text NOT NULL CHECK (length(finished_serial_or_batch) BETWEEN 1 AND 100),
  component_item_id uuid NOT NULL,
  tracking text NOT NULL CHECK (tracking IN ('serial','batch')),
  component_serial_or_batch text NOT NULL CHECK (length(component_serial_or_batch) BETWEEN 1 AND 100),
  qty numeric(18,6) NOT NULL CHECK (qty > 0),
  journal_id uuid,
  raf_journal_id uuid,
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  assigned_by text NOT NULL,
  assigned_at timestamptz NOT NULL DEFAULT now(),
  detached_by text,
  detached_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, order_id) REFERENCES production_order (tenant_id, id),
  CHECK (tracking <> 'serial' OR qty = 1),
  CHECK ((detached_at IS NULL) = (detached_by IS NULL))
);
-- BR-21.14: each serialized component serial belongs to exactly one finished serial.
CREATE UNIQUE INDEX IF NOT EXISTS production_as_built_component_serial
  ON production_as_built (tenant_id, component_item_id, component_serial_or_batch)
  WHERE tracking = 'serial' AND detached_at IS NULL;
CREATE INDEX IF NOT EXISTS production_as_built_finished ON production_as_built (tenant_id, finished_serial_or_batch);
CREATE INDEX IF NOT EXISTS production_as_built_component ON production_as_built (tenant_id, component_serial_or_batch);
CREATE INDEX IF NOT EXISTS production_as_built_order ON production_as_built (tenant_id, order_id);

ALTER TABLE production_as_built ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_as_built FORCE ROW LEVEL SECURITY;
CREATE POLICY production_as_built_tenant_access ON production_as_built AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY production_as_built_legal_entity_scope ON production_as_built AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON production_as_built TO passage_app';
  END IF;
END
$$;
