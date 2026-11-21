CREATE TABLE IF NOT EXISTS receiving_matching_policy (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  vendor_id uuid,
  item_id uuid,
  match_level text NOT NULL CHECK (match_level IN ('NONE', 'TWO_WAY', 'THREE_WAY')),
  price_tol_pct numeric(20,6) NOT NULL CHECK (price_tol_pct >= 0),
  price_tol_abs numeric(20,6) NOT NULL CHECK (price_tol_abs >= 0),
  qty_tol_pct numeric(20,6) NOT NULL CHECK (qty_tol_pct >= 0),
  total_tol_pct numeric(20,6) NOT NULL CHECK (total_tol_pct >= 0),
  total_tol_abs numeric(20,6) NOT NULL CHECK (total_tol_abs >= 0),
  small_diff_abs numeric(20,6) NOT NULL CHECK (small_diff_abs >= 0),
  price_tol_mode text NOT NULL CHECK (price_tol_mode IN ('UPPER_ONLY', 'BOTH')),
  owner_user_id text NOT NULL CHECK (char_length(owner_user_id) BETWEEN 1 AND 200),
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS receiving_matching_policy_scope_ix ON receiving_matching_policy (tenant_id, legal_entity_id);

CREATE TABLE IF NOT EXISTS receiving_invoice_exception (
  tenant_id uuid NOT NULL,
  invoice_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  assignee text NOT NULL CHECK (char_length(assignee) BETWEEN 1 AND 200),
  reasons text NOT NULL,
  status text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'APPROVED', 'REJECTED')),
  created_at timestamptz NOT NULL DEFAULT now(),
  decided_by text,
  decided_at timestamptz,
  decision_reason text NOT NULL DEFAULT '' CHECK (char_length(decision_reason) <= 500),
  PRIMARY KEY (tenant_id, invoice_id),
  FOREIGN KEY (tenant_id, invoice_id) REFERENCES receiving_vendor_invoice (tenant_id, id),
  CHECK ((status = 'OPEN') = (decided_at IS NULL))
);
CREATE INDEX IF NOT EXISTS receiving_invoice_exception_status_ix ON receiving_invoice_exception (tenant_id, status, created_at, invoice_id);

DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['receiving_matching_policy', 'receiving_invoice_exception'] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = t AND policyname = t || '_tenant_access') THEN
      EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
      EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
      EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''depot.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''depot.tenant_id'', true)::uuid)', t || '_tenant_access', t);
      EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''depot.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''depot.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
    END IF;
  END LOOP;
END
$$;

GRANT SELECT, INSERT, UPDATE ON receiving_matching_policy TO depot_app;
GRANT SELECT, INSERT ON receiving_invoice_exception TO depot_app;
GRANT UPDATE (status, decided_by, decided_at, decision_reason) ON receiving_invoice_exception TO depot_app;
