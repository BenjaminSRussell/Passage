-- Wave 44, FR-44.11: work order costs are booked in the general ledger. The posting profile
-- maps a legal entity and work order type to the maintenance expense and offset accounts
-- (type ALL is the wildcard); with no row the service uses its documented placeholder
-- accounts. eam_gl_posting is the append-only subledger of what was booked, one row per
-- ledger line, so the accounts can be reconciled to the ledger's balances.

CREATE TABLE IF NOT EXISTS eam_posting_profile (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  work_order_type text NOT NULL CHECK (work_order_type = 'ALL' OR work_order_type IN ('PREVENTIVE', 'CORRECTIVE', 'INSPECTION', 'EMERGENCY', 'CAPITAL', 'ROUND', 'REFURBISHMENT')),
  parts_expense_account text NOT NULL CHECK (char_length(parts_expense_account) BETWEEN 1 AND 40),
  labor_expense_account text NOT NULL CHECK (char_length(labor_expense_account) BETWEEN 1 AND 40),
  parts_offset_account text NOT NULL CHECK (char_length(parts_offset_account) BETWEEN 1 AND 40),
  labor_offset_account text NOT NULL CHECK (char_length(labor_offset_account) BETWEEN 1 AND 40),
  version integer NOT NULL DEFAULT 1,
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, work_order_type)
);

-- The basis is the exact cost the booked entries stand for (rounded to the currency's minor unit when booked).
ALTER TABLE work_order
  ADD COLUMN IF NOT EXISTS gl_parts_basis numeric(20,6) NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS gl_labor_basis numeric(20,6) NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS gl_post_count integer NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS eam_gl_posting (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  work_order_id uuid NOT NULL,
  seq integer NOT NULL CHECK (seq >= 1),
  account text NOT NULL,
  amount_dr numeric(20,6) NOT NULL DEFAULT 0 CHECK (amount_dr >= 0),
  amount_cr numeric(20,6) NOT NULL DEFAULT 0 CHECK (amount_cr >= 0),
  currency char(3) NOT NULL,
  posting_date date NOT NULL,
  journal_entry_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK ((amount_dr = 0) <> (amount_cr = 0)),
  FOREIGN KEY (tenant_id, work_order_id) REFERENCES work_order (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS eam_gl_posting_order ON eam_gl_posting (tenant_id, work_order_id, seq);
CREATE INDEX IF NOT EXISTS eam_gl_posting_account ON eam_gl_posting (tenant_id, legal_entity_id, currency, account);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['eam_posting_profile', 'eam_gl_posting'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('DROP POLICY IF EXISTS %I_tenant_access ON %I', t, t);
    EXECUTE format('CREATE POLICY %I_tenant_access ON %I AS PERMISSIVE
      USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)
      WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t, t);
  END LOOP;
END $$;

-- The profile is updated under row lock; the GL subledger is insert-only.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON eam_posting_profile TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON eam_gl_posting TO passage_app';
  END IF;
END
$$;
