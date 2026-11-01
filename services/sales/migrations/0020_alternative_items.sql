-- Wave 27 slice 16: alternative items (FR-27.13). A rule names a product to
-- offer in place of ordered_product_id when the ordered product cannot be
-- delivered in full by the requested date, for one customer or (customer NULL)
-- every customer of the legal entity. priority orders the offers (1 first);
-- valid_from / valid_to bound the days on which the rule applies, judged on
-- the requested delivery date. The offer itself is computed from ATP buckets
-- supplied with the request and stores nothing.

CREATE TABLE IF NOT EXISTS alternative_item_rule (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  ordered_product_id uuid NOT NULL,
  alternative_product_id uuid NOT NULL,
  customer_id uuid,
  priority smallint NOT NULL DEFAULT 1 CHECK (priority BETWEEN 1 AND 99),
  valid_from date,
  valid_to date,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK (ordered_product_id <> alternative_product_id),
  CHECK (valid_from IS NULL OR valid_to IS NULL OR valid_from <= valid_to)
);
-- One rule per (ordered product, customer or all customers, alternative).
CREATE UNIQUE INDEX IF NOT EXISTS alternative_item_rule_unique ON alternative_item_rule
  (tenant_id, legal_entity_id, ordered_product_id, coalesce(customer_id, '00000000-0000-0000-0000-000000000000'::uuid), alternative_product_id);

ALTER TABLE alternative_item_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE alternative_item_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY alternative_item_rule_tenant_access ON alternative_item_rule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY alternative_item_rule_legal_entity_scope ON alternative_item_rule AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON alternative_item_rule TO passage_app';
  END IF;
END
$$;
