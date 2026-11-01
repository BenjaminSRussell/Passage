-- Process manufacturing, wave 24 slice 14: customer batch requirements
-- (FR-24.19, BR-24.13, AC-24.14). One row per rule for an item: the customer's
-- (or customer group's, or everyone's) minimum remaining shelf life in days at
-- shipment, a numeric range an attribute value must lie in, and the same-batch
-- flag. A row sets at least one of the three; the most specific row that sets a
-- field wins (customer over customer group over item default) and, within one
-- scope, the row that started last, then the one created last. A rule is never
-- edited; it is retired and replaced. customer_id and customer_group_id refer
-- to party and sales master data in other services (no foreign key).

CREATE TABLE IF NOT EXISTS customer_batch_requirement (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  item_id uuid NOT NULL,
  customer_id uuid,
  customer_group_id uuid,
  sellable_days integer CHECK (sellable_days BETWEEN 0 AND 3650),
  attribute text CHECK (char_length(attribute) BETWEEN 1 AND 40),
  min_value numeric(18,6),
  max_value numeric(18,6),
  same_batch boolean,
  valid_from date,
  valid_to date,
  retired_at timestamptz,
  retired_by text,
  version integer NOT NULL DEFAULT 1 CHECK (version > 0),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK (customer_id IS NULL OR customer_group_id IS NULL),
  CHECK (sellable_days IS NOT NULL OR attribute IS NOT NULL OR same_batch IS NOT NULL),
  CHECK ((attribute IS NULL) = (min_value IS NULL AND max_value IS NULL)),
  CHECK (min_value IS NULL OR max_value IS NULL OR min_value <= max_value),
  CHECK (valid_from IS NULL OR valid_to IS NULL OR valid_from <= valid_to),
  CHECK ((retired_at IS NULL) = (retired_by IS NULL))
);
CREATE INDEX IF NOT EXISTS customer_batch_requirement_item_idx ON customer_batch_requirement (tenant_id, item_id, created_at);

ALTER TABLE customer_batch_requirement ENABLE ROW LEVEL SECURITY;
ALTER TABLE customer_batch_requirement FORCE ROW LEVEL SECURITY;
CREATE POLICY customer_batch_requirement_tenant_access ON customer_batch_requirement AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY customer_batch_requirement_legal_entity_scope ON customer_batch_requirement AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON customer_batch_requirement TO passage_app';
  END IF;
END
$$;
