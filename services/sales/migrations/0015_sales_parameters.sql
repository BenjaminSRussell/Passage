-- Wave 27 slice 11: per-legal-entity sales parameters (PRD 5.27.13).
-- One row per legal entity; a legal entity without a row uses the defaults in
-- code (30, 50, 7, 2, ORDER), which are also the column defaults. Each value is
-- read by a flow: quotation expiry and probability by quotation create, credit
-- release validity by the CREDIT hold release, the hold SLA by the hold queue,
-- default order type by order create. Column names and ranges are the PRD's.

CREATE TABLE IF NOT EXISTS sales_parameter (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  quotation_expiry_days int NOT NULL DEFAULT 30 CHECK (quotation_expiry_days BETWEEN 1 AND 365),
  quotation_probability_default int NOT NULL DEFAULT 50 CHECK (quotation_probability_default BETWEEN 0 AND 100),
  credit_release_validity_days int NOT NULL DEFAULT 7 CHECK (credit_release_validity_days BETWEEN 1 AND 30),
  credit_hold_sla_business_days int NOT NULL DEFAULT 2 CHECK (credit_hold_sla_business_days BETWEEN 1 AND 10),
  default_order_type text NOT NULL DEFAULT 'ORDER' CHECK (default_order_type IN ('JOURNAL', 'ORDER', 'SUBSCRIPTION')),
  version int NOT NULL DEFAULT 1,
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id)
);

ALTER TABLE sales_parameter ENABLE ROW LEVEL SECURITY;
ALTER TABLE sales_parameter FORCE ROW LEVEL SECURITY;
CREATE POLICY sales_parameter_tenant_access ON sales_parameter AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY sales_parameter_legal_entity_scope ON sales_parameter AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The store inserts a row and updates it by version; it never deletes.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON sales_parameter TO passage_app';
  END IF;
END
$$;
