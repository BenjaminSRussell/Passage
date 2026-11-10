-- Wave 40 (#40-3, #40-5): posted intercompany journals and IC charges. A
-- journal is one pair id with one ledger entry per entity; its status moves
-- pending -> posted, or pending -> compensated when a later side is refused and
-- the earlier sides are reversed (FR-40.5). A charge request is stored per
-- source and version (FR-40.13): a source that has no transfer price is kept
-- on suspense until the next version reprices it (BR-40.12). Every table
-- carries tenant_id and is tenant-scoped by row-level security.

CREATE TABLE IF NOT EXISTS ic_journal (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  pair_id uuid NOT NULL,
  source_type text NOT NULL CHECK (source_type IN ('journal', 'charge')),
  source_ref text NOT NULL CHECK (length(source_ref) BETWEEN 1 AND 255),
  idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 255),
  request_hash text NOT NULL,
  posting_date date NOT NULL,
  status text NOT NULL CHECK (status IN ('pending', 'posted', 'compensated')),
  failure_code text,
  failure_detail text,
  created_by text NOT NULL CHECK (length(created_by) >= 1),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  UNIQUE (tenant_id, source_type, source_ref)
);
CREATE INDEX IF NOT EXISTS ic_journal_recent ON ic_journal (tenant_id, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS ic_journal_entry (
  tenant_id uuid NOT NULL,
  journal_id uuid NOT NULL,
  seq integer NOT NULL,
  entity text NOT NULL CHECK (length(entity) BETWEEN 1 AND 64),
  legal_entity_id uuid NOT NULL,
  ledger_id text NOT NULL CHECK (length(ledger_id) BETWEEN 1 AND 64),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  legs jsonb NOT NULL,
  ledger_entry_id text,
  reversal_entry_id text,
  PRIMARY KEY (tenant_id, journal_id, seq),
  FOREIGN KEY (tenant_id, journal_id) REFERENCES ic_journal (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS ic_charge_request (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  source_id text NOT NULL CHECK (length(source_id) BETWEEN 1 AND 128),
  version integer NOT NULL CHECK (version >= 1),
  request_hash text NOT NULL,
  lender text NOT NULL,
  borrower text NOT NULL,
  category text NOT NULL CHECK (category IN ('Expense', 'Hour', 'Item', 'Fee')),
  resource_role text NOT NULL DEFAULT '',
  transaction_date date NOT NULL,
  quantity numeric(28,6) NOT NULL CHECK (quantity > 0),
  unit_cost numeric(28,6) NOT NULL CHECK (unit_cost >= 0),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  status text NOT NULL CHECK (status IN ('pending', 'posted', 'compensated', 'suspense')),
  method text,
  cost numeric(28,6),
  price numeric(28,6),
  margin numeric(28,6),
  journal_id uuid,
  failure_code text,
  created_by text NOT NULL CHECK (length(created_by) >= 1),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, source_id, version),
  FOREIGN KEY (tenant_id, journal_id) REFERENCES ic_journal (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS ic_charge_recent ON ic_charge_request (tenant_id, created_at DESC, id DESC);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['ic_journal', 'ic_journal_entry', 'ic_charge_request'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format($p$CREATE POLICY %I_tenant_access ON %I AS PERMISSIVE
      USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
      WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid)$p$, t, t);
  END LOOP;
END
$$;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON ic_journal, ic_journal_entry, ic_charge_request TO passage_app';
  END IF;
END
$$;
