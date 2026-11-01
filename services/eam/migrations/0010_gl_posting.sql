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
-- An asset carries the single legal entity scope of the caller that created it; a work order takes its entity from its asset when it is created
-- (creation is refused when the asset has none), so the cost is booked into that entity's ledger. NULL entities are reported by the GL reconciliation.
ALTER TABLE asset ADD COLUMN IF NOT EXISTS legal_entity_id uuid;
-- Existing assets: the eam schema holds no legal entity anywhere (sites, cost centers and the creating scope are not stored), so the only link is a
-- site whose other assets already name exactly one legal entity. The rest are listed by the GL reconciliation and set with PUT /v1/eam/assets/{id}/legal-entity.
UPDATE asset a SET legal_entity_id = s.legal_entity_id
  FROM (SELECT tenant_id, site_id, min(legal_entity_id::text)::uuid AS legal_entity_id FROM asset WHERE legal_entity_id IS NOT NULL
        GROUP BY tenant_id, site_id HAVING count(DISTINCT legal_entity_id) = 1) s
  WHERE a.tenant_id = s.tenant_id AND a.site_id = s.site_id AND a.legal_entity_id IS NULL;
ALTER TABLE work_order
  ADD COLUMN IF NOT EXISTS legal_entity_id uuid,
  ADD COLUMN IF NOT EXISTS gl_parts_basis numeric(20,6) NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS gl_labor_basis numeric(20,6) NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS gl_post_count integer NOT NULL DEFAULT 0;

UPDATE work_order w SET legal_entity_id = a.legal_entity_id
  FROM asset a WHERE a.tenant_id = w.tenant_id AND a.id = w.asset_id AND w.legal_entity_id IS NULL AND a.legal_entity_id IS NOT NULL;

-- A booking is written here, in the transaction that decides it, before the ledger is called; retries resend exactly this payload and the row is
-- deleted when the ledger has confirmed it and eam_gl_posting holds its lines.
CREATE TABLE IF NOT EXISTS eam_pending_booking (
  tenant_id uuid NOT NULL,
  work_order_id uuid NOT NULL,
  seq integer NOT NULL CHECK (seq >= 1),
  legal_entity_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  currency char(3) NOT NULL,
  posting_date date NOT NULL,
  idempotency_key text NOT NULL,
  lines jsonb NOT NULL,
  parts_basis numeric(20,6) NOT NULL,
  labor_basis numeric(20,6) NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, work_order_id),
  FOREIGN KEY (tenant_id, work_order_id) REFERENCES work_order (tenant_id, id)
);

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

DROP POLICY IF EXISTS eam_pending_booking_legal_entity_scope ON eam_pending_booking;
CREATE POLICY eam_pending_booking_legal_entity_scope ON eam_pending_booking AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['eam_posting_profile', 'eam_gl_posting', 'eam_pending_booking'] LOOP
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
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON eam_pending_booking TO passage_app';
  END IF;
END
$$;
