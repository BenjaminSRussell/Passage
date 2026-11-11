-- Wave 40 (#40, round 3): what the earlier slices took in every request is stored. An entity registry (the legal entity, ledger,
-- currency, country, FX-difference and tax accounts of each entity code), the IC accounting matrix, effective-dated transfer
-- prices and the tenant's charge settings; reconciliation runs can then read the pair amounts from the ledgers, charges price
-- from the stored prices, and a posting into a closed period records the date it moved to. Every table carries tenant_id and is
-- tenant-scoped by row-level security.

CREATE TABLE IF NOT EXISTS ic_entity (
  tenant_id uuid NOT NULL,
  entity text NOT NULL CHECK (length(entity) BETWEEN 1 AND 64),
  legal_entity_id uuid NOT NULL,
  ledger_id text NOT NULL CHECK (length(ledger_id) BETWEEN 1 AND 64),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  country text NOT NULL DEFAULT '' CHECK (country = '' OR country ~ '^[A-Z]{2}$'),
  fx_difference_account text NOT NULL DEFAULT '' CHECK (length(fx_difference_account) <= 64),
  output_tax_account text NOT NULL DEFAULT '' CHECK (length(output_tax_account) <= 64),
  input_tax_account text NOT NULL DEFAULT '' CHECK (length(input_tax_account) <= 64),
  tax_group text NOT NULL DEFAULT '' CHECK (length(tax_group) <= 64),
  updated_by text NOT NULL CHECK (length(updated_by) >= 1),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, entity)
);

CREATE TABLE IF NOT EXISTS ic_matrix_row (
  tenant_id uuid NOT NULL,
  entity_a text NOT NULL,
  entity_b text NOT NULL CHECK (entity_b <> entity_a),
  due_from text NOT NULL CHECK (length(due_from) BETWEEN 1 AND 64),
  due_to text NOT NULL CHECK (length(due_to) BETWEEN 1 AND 64),
  updated_by text NOT NULL CHECK (length(updated_by) >= 1),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, entity_a, entity_b),
  FOREIGN KEY (tenant_id, entity_a) REFERENCES ic_entity (tenant_id, entity),
  FOREIGN KEY (tenant_id, entity_b) REFERENCES ic_entity (tenant_id, entity)
);

CREATE TABLE IF NOT EXISTS ic_transfer_price (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  lender text NOT NULL CHECK (length(lender) BETWEEN 1 AND 64),
  borrower text NOT NULL CHECK (length(borrower) BETWEEN 1 AND 64 AND borrower <> lender),
  category text NOT NULL CHECK (category IN ('Expense', 'Hour', 'Item', 'Fee')),
  resource_role text NOT NULL DEFAULT '' CHECK (length(resource_role) <= 64),
  method text NOT NULL CHECK (method IN ('Cost', 'CostPlus', 'Table')),
  markup_pct numeric(9,4) NOT NULL DEFAULT 0 CHECK (markup_pct >= 0),
  unit_price numeric(28,6) NOT NULL DEFAULT 0 CHECK (unit_price >= 0),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  effective_from date NOT NULL,
  created_by text NOT NULL CHECK (length(created_by) >= 1),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, lender, borrower, category, resource_role, effective_from)
);

CREATE TABLE IF NOT EXISTS ic_setting (
  tenant_id uuid PRIMARY KEY,
  charge_settlement_mode text NOT NULL DEFAULT 'Journal' CHECK (charge_settlement_mode IN ('Journal', 'Invoice')),
  cross_border_tax boolean NOT NULL DEFAULT false,
  -- What the scheduled reconciliation compares in: the currency both sides are converted to, and the FX tolerance (BR-40.5).
  recon_currency text NOT NULL DEFAULT '' CHECK (recon_currency = '' OR recon_currency ~ '^[A-Z]{3}$'),
  fx_tolerance_pct numeric(6,3) NOT NULL DEFAULT 0.5 CHECK (fx_tolerance_pct >= 0 AND fx_tolerance_pct <= 5),
  updated_by text NOT NULL CHECK (length(updated_by) >= 1),
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- A side posted into a closed period lands in the first open one: posted_on is the date the ledger took it, shifted_from the date
-- it was meant for.
ALTER TABLE ic_journal_entry ADD COLUMN IF NOT EXISTS posted_on date;
ALTER TABLE ic_journal_entry ADD COLUMN IF NOT EXISTS shifted_from date;

-- Charges: a charge in Invoice mode is handed to the invoice documents instead of being posted; a cross-border charge carries the
-- tax the tax service worked out.
ALTER TABLE ic_charge_request DROP CONSTRAINT IF EXISTS ic_charge_request_status_check;
ALTER TABLE ic_charge_request ADD CONSTRAINT ic_charge_request_status_check
  CHECK (status IN ('pending', 'posted', 'compensated', 'suspense', 'invoice_requested'));
ALTER TABLE ic_charge_request ADD COLUMN IF NOT EXISTS settlement_mode text NOT NULL DEFAULT 'Journal';
ALTER TABLE ic_charge_request ADD COLUMN IF NOT EXISTS tax_amount numeric(28,6) NOT NULL DEFAULT 0;
ALTER TABLE ic_charge_request ADD COLUMN IF NOT EXISTS tax_code text NOT NULL DEFAULT '';
ALTER TABLE ic_charge_request ADD COLUMN IF NOT EXISTS tax_recorded boolean NOT NULL DEFAULT false;

-- Reconciliation runs say where their pair amounts came from.
ALTER TABLE ic_reconciliation_run ADD COLUMN IF NOT EXISTS source text NOT NULL DEFAULT 'request';

-- The FX difference of a reconciliation item, booked to the entity's FX-difference account. One per item.
CREATE TABLE IF NOT EXISTS ic_fx_adjustment (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  item_id uuid NOT NULL,
  entity text NOT NULL,
  legal_entity_id uuid NOT NULL,
  ledger_id text NOT NULL,
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  amount numeric(28,6) NOT NULL CHECK (amount > 0),
  increases_balance boolean NOT NULL,
  balance_account text NOT NULL,
  fx_account text NOT NULL,
  period_end date NOT NULL,
  status text NOT NULL CHECK (status IN ('pending', 'posted')),
  ledger_entry_id text,
  posted_on date,
  created_by text NOT NULL CHECK (length(created_by) >= 1),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, item_id)
);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['ic_entity', 'ic_matrix_row', 'ic_transfer_price', 'ic_setting', 'ic_fx_adjustment'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format($p$CREATE POLICY %I_tenant_access ON %I AS PERMISSIVE
      USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
      WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid)$p$, t, t);
  END LOOP;
END
$$;

-- A caller sees and writes only the registry entries and FX adjustments of legal entities in its scope, like the journal entries.
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['ic_entity', 'ic_fx_adjustment'] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename = t AND policyname = t || '_legal_entity_scope') THEN
      EXECUTE format($p$CREATE POLICY %I ON %I AS RESTRICTIVE
        USING (current_setting('passage.legal_entity_scopes', true) = '*'
               OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))$p$, t || '_legal_entity_scope', t);
    END IF;
  END LOOP;
END
$$;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON ic_entity, ic_matrix_row TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON ic_transfer_price TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON ic_setting, ic_fx_adjustment TO passage_app';
  END IF;
END
$$;
