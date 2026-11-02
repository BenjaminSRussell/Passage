-- Pricing context, wave 26 slice 11: commodity pricing (FR-26.21). A cost basis type holds
-- dated, versioned index values; a price calculation turns the latest index into a price
-- (index x conversion factor + margin) for the ACTIVE PRICE records it names; a run
-- previews or creates a VALIDATED price journal that the existing journal post applies.
-- Additive only.

CREATE TABLE IF NOT EXISTS commodity_basis (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9_-]{1,32}$'),
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  unit text NOT NULL CHECK (length(unit) BETWEEN 1 AND 16),
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, code),
  UNIQUE (tenant_id, idempotency_key)
);

-- Index values are append-only: a correction is a new revision of the same day, and the
-- highest revision is the one a run reads.
CREATE TABLE IF NOT EXISTS commodity_index_value (
  tenant_id uuid NOT NULL,
  basis_id uuid NOT NULL,
  as_of date NOT NULL,
  revision integer NOT NULL CHECK (revision >= 1),
  legal_entity_id uuid NOT NULL,
  value numeric(19,6) NOT NULL CHECK (value > 0),
  source text NOT NULL DEFAULT '' CHECK (length(source) <= 120),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, basis_id, as_of, revision),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, basis_id) REFERENCES commodity_basis (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS commodity_calculation (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  basis_id uuid NOT NULL,
  factor numeric(19,6) NOT NULL CHECK (factor > 0),
  margin_kind text NOT NULL CHECK (margin_kind IN ('PERCENT', 'AMOUNT')),
  margin numeric(19,6) NOT NULL CHECK (margin >= 0),
  rounding integer NOT NULL DEFAULT 6 CHECK (rounding BETWEEN 2 AND 6),
  max_index_age_days integer NOT NULL DEFAULT 7 CHECK (max_index_age_days BETWEEN 1 AND 365),
  customer_kind text NOT NULL CHECK (customer_kind IN ('customer', 'customer_group', 'all')),
  customer_key text NOT NULL DEFAULT '' CHECK (length(customer_key) <= 64),
  product_id text NOT NULL CHECK (length(product_id) BETWEEN 1 AND 64),
  unit text NOT NULL CHECK (length(unit) BETWEEN 1 AND 16),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  withdrawn boolean NOT NULL DEFAULT false,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, basis_id) REFERENCES commodity_basis (tenant_id, id)
);

-- One row per run that reached the index lookup and was not a preview. journal_id is empty
-- when no ACTIVE record changed price. The index day, revision and value the run used are
-- kept, so a later correction of a past quote never changes what a past run priced from.
-- run_by is the user who ran it: that user cannot post the run's journal (CommodityPriceRun
-- conflicts with PricingJournalPost, PRD 5.26.17).
CREATE TABLE IF NOT EXISTS commodity_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  calculation_id uuid NOT NULL,
  as_of date NOT NULL,
  index_as_of date NOT NULL,
  index_revision integer NOT NULL,
  index_value numeric(19,6) NOT NULL,
  price numeric(19,6) NOT NULL,
  changed_count integer NOT NULL CHECK (changed_count >= 0),
  unchanged_count integer NOT NULL CHECK (unchanged_count >= 0),
  stale boolean NOT NULL DEFAULT false,
  journal_id uuid,
  run_by text NOT NULL DEFAULT '' CHECK (length(run_by) <= 128),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, calculation_id) REFERENCES commodity_calculation (tenant_id, id),
  FOREIGN KEY (tenant_id, journal_id) REFERENCES price_journal (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS commodity_run_calc_ix ON commodity_run (tenant_id, calculation_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS commodity_run_journal_ux ON commodity_run (tenant_id, journal_id) WHERE journal_id IS NOT NULL;

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['commodity_basis', 'commodity_index_value', 'commodity_calculation', 'commodity_run'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format($p$CREATE POLICY %I ON %I AS PERMISSIVE
      USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
      WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid)$p$, t || '_tenant_access', t);
    EXECUTE format($p$CREATE POLICY %I ON %I AS RESTRICTIVE
      USING (current_setting('passage.legal_entity_scopes', true) = '*'
             OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))$p$,
      t || '_legal_entity_scope', t);
  END LOOP;
END $$;

-- Application role privileges. The store locks commodity_basis (FOR UPDATE) to number index
-- revisions and updates calculations (withdraw). Index values and runs are append-only.
-- Skipped when the role does not exist.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON commodity_basis, commodity_calculation TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON commodity_index_value, commodity_run TO passage_app';
  END IF;
END
$$;
