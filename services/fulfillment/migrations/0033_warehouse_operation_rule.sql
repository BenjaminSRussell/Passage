-- Fulfillment slice 26: the posting and packing parameters per warehouse
-- (section 5.28.13: posting_date_source, auto_pack_enabled,
-- workbench_group_key_default; BR-28.3 names the warehouse time zone). A
-- warehouse belongs to one legal entity, so the rule carries it and is scoped
-- by it (the shape of warehouse_release_rule).
--   posting_date_source: SHIP_TIMESTAMP (always the day of the post in the
--   warehouse time zone), TODAY (that day by default, the poster may name
--   another) or MANUAL (the poster names the date). A warehouse without a saved rule reads MANUAL, which is what
--   every post did before this migration.
CREATE TABLE IF NOT EXISTS warehouse_operation_rule (
  tenant_id uuid NOT NULL,
  warehouse_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  posting_date_source text NOT NULL DEFAULT 'MANUAL' CHECK (posting_date_source IN ('SHIP_TIMESTAMP', 'TODAY', 'MANUAL')),
  time_zone text NOT NULL DEFAULT 'UTC' CHECK (length(time_zone) BETWEEN 1 AND 64),
  auto_pack_enabled boolean NOT NULL DEFAULT true,
  workbench_group_key_default text NOT NULL DEFAULT 'CUSTOMER' CHECK (workbench_group_key_default IN ('CUSTOMER', 'CARRIER', 'ZONE', 'PRODUCT')),
  version integer NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, warehouse_id)
);

ALTER TABLE warehouse_operation_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE warehouse_operation_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY warehouse_operation_rule_tenant_access ON warehouse_operation_rule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY warehouse_operation_rule_legal_entity_scope ON warehouse_operation_rule AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON warehouse_operation_rule TO passage_app';
  END IF;
END
$$;
