-- Wave 21 slice 2: posting profiles (FR-21.13), production journals (FR-21.5 to
-- FR-21.7), the WIP subledger and the variances posted at End (FR-21.8).
-- Additive: slice-1 tables gain columns with defaults.

ALTER TABLE production_order
  ADD COLUMN IF NOT EXISTS item_group text NOT NULL DEFAULT '';

ALTER TABLE production_bom_line
  ADD COLUMN IF NOT EXISTS flushing_principle text NOT NULL DEFAULT 'manual'
    CHECK (flushing_principle IN ('manual','start','finish','available-at-location')),
  ADD COLUMN IF NOT EXISTS qty_consumed numeric(18,6) NOT NULL DEFAULT 0;

ALTER TABLE production_operation
  ADD COLUMN IF NOT EXISTS hours_reported numeric(10,2) NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS qty_good numeric(18,6) NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS qty_error numeric(18,6) NOT NULL DEFAULT 0;

-- Ledger accounts per legal entity × item group × site; 'All' is the wildcard.
CREATE TABLE IF NOT EXISTS production_posting_profile (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  item_group text NOT NULL,
  site_key text NOT NULL,
  wip_account text NOT NULL,
  inventory_account text NOT NULL,
  finished_goods_account text NOT NULL,
  labor_absorption_account text NOT NULL,
  overhead_absorption_account text NOT NULL,
  lot_size_variance_account text NOT NULL,
  quantity_variance_account text NOT NULL,
  price_variance_account text NOT NULL,
  substitution_variance_account text NOT NULL,
  version integer NOT NULL DEFAULT 1,
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, item_group, site_key)
);

CREATE TABLE IF NOT EXISTS production_journal_sequence (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  next_value bigint NOT NULL DEFAULT 1,
  PRIMARY KEY (tenant_id, legal_entity_id)
);

-- A journal is a draft until posted. A reversal journal (FR-21.9) is posted
-- at creation and points at the journal it reverses; one reversal per journal.
CREATE TABLE IF NOT EXISTS production_journal (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  order_id uuid NOT NULL,
  journal_type text NOT NULL CHECK (journal_type IN ('picking-list','route-card','report-as-finished')),
  journal_number text NOT NULL,
  posting_date date NOT NULL,
  posted boolean NOT NULL DEFAULT false,
  posted_at timestamptz,
  posted_by text,
  qty_good numeric(18,6) NOT NULL DEFAULT 0,
  qty_error numeric(18,6) NOT NULL DEFAULT 0,
  reversal_of uuid,
  journal_entry_id uuid,
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  post_key text,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, journal_number),
  UNIQUE (tenant_id, idempotency_key),
  UNIQUE (tenant_id, post_key),
  UNIQUE (tenant_id, reversal_of),
  FOREIGN KEY (tenant_id, order_id) REFERENCES production_order (tenant_id, id),
  FOREIGN KEY (tenant_id, reversal_of) REFERENCES production_journal (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS production_journal_order ON production_journal (tenant_id, order_id, created_at);

-- Issue lines carry the BOM line and item; hours lines the operation. Quantities
-- and amounts are negative on reversal lines only.
CREATE TABLE IF NOT EXISTS production_journal_line (
  tenant_id uuid NOT NULL,
  journal_id uuid NOT NULL,
  line_seq integer NOT NULL,
  kind text NOT NULL CHECK (kind IN ('issue','hours','receipt')),
  bom_line_number text,
  oper_number integer,
  item_id uuid,
  qty numeric(18,6) NOT NULL,
  qty_good numeric(18,6) NOT NULL DEFAULT 0,
  qty_error numeric(18,6) NOT NULL DEFAULT 0,
  finish_operation boolean NOT NULL DEFAULT false,
  unit_cost numeric(20,6) NOT NULL DEFAULT 0,
  amount numeric(20,6) NOT NULL DEFAULT 0,
  overhead_amount numeric(20,6) NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, journal_id, line_seq),
  FOREIGN KEY (tenant_id, journal_id) REFERENCES production_journal (tenant_id, id),
  CHECK ((kind = 'issue') = (bom_line_number IS NOT NULL)),
  CHECK ((kind = 'hours') = (oper_number IS NOT NULL))
);

-- The WIP subledger (PRD 4.21.4 WipPosting): one balanced set of rows per
-- posted journal or settlement. Rows with wip = true are on the order's WIP
-- account; their Σ dr − Σ cr is the order's work in progress, 0 once Ended.
CREATE TABLE IF NOT EXISTS production_wip_posting (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  order_id uuid NOT NULL,
  journal_id uuid,
  posting_type text NOT NULL,
  ledger_account text NOT NULL,
  wip boolean NOT NULL,
  amount_dr numeric(20,6) NOT NULL DEFAULT 0 CHECK (amount_dr >= 0),
  amount_cr numeric(20,6) NOT NULL DEFAULT 0 CHECK (amount_cr >= 0),
  currency char(3) NOT NULL,
  posting_date date NOT NULL,
  journal_entry_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, order_id) REFERENCES production_order (tenant_id, id),
  FOREIGN KEY (tenant_id, journal_id) REFERENCES production_journal (tenant_id, id),
  CHECK ((amount_dr = 0) <> (amount_cr = 0))
);
CREATE INDEX IF NOT EXISTS production_wip_posting_order ON production_wip_posting (tenant_id, order_id);

CREATE TABLE IF NOT EXISTS production_variance (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  order_id uuid NOT NULL,
  variance_type text NOT NULL CHECK (variance_type IN ('lot-size','production-quantity','production-price','production-substitution')),
  cost_group text NOT NULL CHECK (cost_group IN ('material','labor','overhead','subcontract')),
  amount numeric(20,6) NOT NULL,
  currency char(3) NOT NULL,
  journal_entry_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, order_id, variance_type, cost_group),
  FOREIGN KEY (tenant_id, order_id) REFERENCES production_order (tenant_id, id)
);

ALTER TABLE production_posting_profile ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_posting_profile FORCE ROW LEVEL SECURITY;
CREATE POLICY production_posting_profile_tenant_access ON production_posting_profile AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY production_posting_profile_legal_entity_scope ON production_posting_profile AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE production_journal_sequence ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_journal_sequence FORCE ROW LEVEL SECURITY;
CREATE POLICY production_journal_sequence_tenant_access ON production_journal_sequence AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY production_journal_sequence_legal_entity_scope ON production_journal_sequence AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE production_journal ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_journal FORCE ROW LEVEL SECURITY;
CREATE POLICY production_journal_tenant_access ON production_journal AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY production_journal_legal_entity_scope ON production_journal AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE production_journal_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_journal_line FORCE ROW LEVEL SECURITY;
CREATE POLICY production_journal_line_tenant_access ON production_journal_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE production_wip_posting ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_wip_posting FORCE ROW LEVEL SECURITY;
CREATE POLICY production_wip_posting_tenant_access ON production_wip_posting AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY production_wip_posting_legal_entity_scope ON production_wip_posting AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE production_variance ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_variance FORCE ROW LEVEL SECURITY;
CREATE POLICY production_variance_tenant_access ON production_variance AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY production_variance_legal_entity_scope ON production_variance AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON
      production_posting_profile, production_journal_sequence, production_journal, production_journal_line,
      production_wip_posting, production_variance
    TO passage_app';
  END IF;
END
$$;
