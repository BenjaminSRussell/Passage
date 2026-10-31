-- Pricing context, wave 26 slice 9: free goods rules (FR-26.22, BR-26.13). A rule gives Free
-- units for every Every units of one product ordered in a unit, inclusive (out of the ordered
-- quantity) or exclusive (on top of it). Additive only.

CREATE TABLE IF NOT EXISTS free_goods_rule (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  customer_kind text NOT NULL CHECK (customer_kind IN ('customer', 'customer_group', 'all')),
  customer_key text NOT NULL DEFAULT '' CHECK (length(customer_key) <= 64),
  product_id text NOT NULL CHECK (length(product_id) BETWEEN 1 AND 64),
  unit text NOT NULL CHECK (length(unit) BETWEEN 1 AND 16),
  kind text NOT NULL CHECK (kind IN ('INCLUSIVE', 'EXCLUSIVE')),
  calc_rule text NOT NULL CHECK (calc_rule IN ('PROPORTIONAL', 'UNIT_REFERENCE', 'WHOLE_UNITS')),
  every_quantity numeric(19,6) NOT NULL CHECK (every_quantity > 0),
  free_quantity numeric(19,6) NOT NULL CHECK (free_quantity > 0),
  min_quantity numeric(19,6) NOT NULL DEFAULT 0 CHECK (min_quantity >= 0),
  valid_from date NOT NULL,
  valid_to date NOT NULL,
  withdrawn boolean NOT NULL DEFAULT false,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (valid_from <= valid_to),
  CHECK (kind = 'EXCLUSIVE' OR free_quantity < every_quantity)
);
CREATE INDEX IF NOT EXISTS free_goods_rule_resolve_ix ON free_goods_rule (tenant_id, legal_entity_id, product_id, unit)
  WHERE NOT withdrawn;

ALTER TABLE free_goods_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE free_goods_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY free_goods_rule_tenant_access ON free_goods_rule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY free_goods_rule_legal_entity_scope ON free_goods_rule AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Application role privileges. The store inserts rules and updates (withdraws) or row-locks
-- them; it never deletes. Skipped when the role does not exist.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON free_goods_rule TO passage_app';
  END IF;
END
$$;
