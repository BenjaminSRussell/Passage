-- Wave 16 slice 1: inventory ledger, dimension sets, on-hand projection, journals.

CREATE TABLE IF NOT EXISTS outbox (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL,
  aggregate_type text NOT NULL,
  aggregate_id text NOT NULL,
  type text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY outbox_tenant_access ON outbox AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Normalized dimension sets. hash = SHA-256 of the canonical dimension string (passage/invdim).
CREATE TABLE IF NOT EXISTS dimension_set (
  tenant_id uuid NOT NULL,
  hash bytea NOT NULL CHECK (octet_length(hash) = 32),
  dims jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, hash)
);
ALTER TABLE dimension_set ENABLE ROW LEVEL SECURITY;
ALTER TABLE dimension_set FORCE ROW LEVEL SECURITY;
CREATE POLICY dimension_set_tenant_access ON dimension_set AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS inventory_journal (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  id uuid NOT NULL,
  journal_type text NOT NULL CHECK (journal_type IN ('MOVEMENT', 'ADJUSTMENT', 'OPENING_BALANCE')),
  status text NOT NULL CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'POSTING', 'POSTED', 'REJECTED', 'CANCELLED')),
  posting_date date NOT NULL,
  offset_account_id text,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  gl_entry jsonb,
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  posted_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK ((journal_type = 'MOVEMENT') = (offset_account_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS inventory_journal_list_idx ON inventory_journal (tenant_id, created_at DESC, id DESC);
ALTER TABLE inventory_journal ENABLE ROW LEVEL SECURITY;
ALTER TABLE inventory_journal FORCE ROW LEVEL SECURITY;
CREATE POLICY inventory_journal_tenant_access ON inventory_journal AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY inventory_journal_legal_entity_scope ON inventory_journal AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS inventory_journal_line (
  tenant_id uuid NOT NULL,
  journal_id uuid NOT NULL,
  id uuid NOT NULL,
  line_no integer NOT NULL CHECK (line_no > 0),
  client_key text CHECK (client_key IS NULL OR length(client_key) BETWEEN 1 AND 255),
  product_id uuid NOT NULL,
  dimension_set_hash bytea NOT NULL,
  dims jsonb NOT NULL,
  qty numeric(28,6) NOT NULL CHECK (qty <> 0),
  entered_qty numeric(28,6) NOT NULL,
  entered_unit text NOT NULL,
  cost_price numeric(20,6) NOT NULL DEFAULT 0 CHECK (cost_price >= 0),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, journal_id, line_no),
  UNIQUE (tenant_id, journal_id, client_key),
  FOREIGN KEY (tenant_id, journal_id) REFERENCES inventory_journal (tenant_id, id)
);
ALTER TABLE inventory_journal_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE inventory_journal_line FORCE ROW LEVEL SECURITY;
CREATE POLICY inventory_journal_line_tenant_access ON inventory_journal_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS inventory_transaction_origin (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  id uuid NOT NULL,
  source_type text NOT NULL CHECK (source_type IN ('INVENT_JOURNAL_LINE', 'PURCH_LINE', 'RECEIPT_LINE', 'SALES_LINE', 'PROD_LINE', 'COUNT_LINE', 'TRANSFER_LINE', 'OPENING')),
  source_id uuid NOT NULL,
  product_id uuid NOT NULL,
  reference_number text,
  direction text NOT NULL CHECK (direction IN ('ISSUE', 'RECEIPT')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, source_type, source_id)
);
ALTER TABLE inventory_transaction_origin ENABLE ROW LEVEL SECURITY;
ALTER TABLE inventory_transaction_origin FORCE ROW LEVEL SECURITY;
CREATE POLICY inventory_transaction_origin_tenant_access ON inventory_transaction_origin AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY inventory_transaction_origin_legal_entity_scope ON inventory_transaction_origin AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Append-only. Status is the current status; history rows arrive with a later slice.
CREATE TABLE IF NOT EXISTS inventory_transaction (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  id uuid NOT NULL,
  origin_id uuid NOT NULL,
  product_id uuid NOT NULL,
  dimension_set_hash bytea NOT NULL,
  qty numeric(28,6) NOT NULL CHECK (qty <> 0),
  entered_qty numeric(28,6) NOT NULL,
  entered_unit text NOT NULL,
  issue_status text CHECK (issue_status IN ('ON_ORDER', 'RESERVED_ORDERED', 'RESERVED_PHYSICAL', 'PICKED', 'DEDUCTED', 'SOLD')),
  receipt_status text CHECK (receipt_status IN ('ORDERED', 'ARRIVED', 'REGISTERED', 'RECEIVED', 'PURCHASED')),
  physical_date date NOT NULL,
  financial_date date,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  cost_amount_physical numeric(20,6) NOT NULL DEFAULT 0,
  cost_amount_posted numeric(20,6),
  cost_amount_adjustment numeric(20,6),
  ledger_entry_id uuid,
  corrects_transaction_id uuid,
  marked_transaction_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, origin_id) REFERENCES inventory_transaction_origin (tenant_id, id),
  FOREIGN KEY (tenant_id, dimension_set_hash) REFERENCES dimension_set (tenant_id, hash),
  CHECK ((issue_status IS NULL) <> (receipt_status IS NULL)),
  CHECK ((qty > 0 AND receipt_status IS NOT NULL) OR (qty < 0 AND issue_status IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS inventory_transaction_product_idx ON inventory_transaction (tenant_id, product_id, dimension_set_hash, physical_date);
CREATE INDEX IF NOT EXISTS inventory_transaction_origin_idx ON inventory_transaction (tenant_id, origin_id);

-- FR-16.1: transactions are append-only. Later slices may move status fields,
-- dates, cost amounts and links; quantity and identity never change and rows
-- are never deleted (corrections are new rows referencing the original).
CREATE OR REPLACE FUNCTION inventory_transaction_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'inventory_transaction is append-only' USING ERRCODE = 'restrict_violation';
  END IF;
  IF (NEW.tenant_id, NEW.legal_entity_id, NEW.id, NEW.origin_id, NEW.product_id, NEW.dimension_set_hash, NEW.qty, NEW.entered_qty,
      NEW.entered_unit, NEW.currency, NEW.created_at)
     IS DISTINCT FROM
     (OLD.tenant_id, OLD.legal_entity_id, OLD.id, OLD.origin_id, OLD.product_id, OLD.dimension_set_hash, OLD.qty, OLD.entered_qty,
      OLD.entered_unit, OLD.currency, OLD.created_at) THEN
    RAISE EXCEPTION 'inventory_transaction quantity and identity are immutable' USING ERRCODE = 'restrict_violation';
  END IF;
  RETURN NEW;
END
$$;
DROP TRIGGER IF EXISTS inventory_transaction_append_only ON inventory_transaction;
CREATE TRIGGER inventory_transaction_append_only BEFORE UPDATE OR DELETE ON inventory_transaction
  FOR EACH ROW EXECUTE FUNCTION inventory_transaction_append_only();
ALTER TABLE inventory_transaction ENABLE ROW LEVEL SECURITY;
ALTER TABLE inventory_transaction FORCE ROW LEVEL SECURITY;
CREATE POLICY inventory_transaction_tenant_access ON inventory_transaction AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY inventory_transaction_legal_entity_scope ON inventory_transaction AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Projection, one row per legal entity x product x dimension set. Updated in the
-- posting transaction. currency is the accounting currency of posted_value, set by
-- the first posting; a later posting in another currency is rejected (INV-CUR-001).
CREATE TABLE IF NOT EXISTS on_hand (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  product_id uuid NOT NULL,
  dimension_set_hash bytea NOT NULL,
  posted_qty numeric(28,6) NOT NULL DEFAULT 0,
  posted_value numeric(20,6) NOT NULL DEFAULT 0,
  received numeric(28,6) NOT NULL DEFAULT 0,
  registered numeric(28,6) NOT NULL DEFAULT 0,
  arrived numeric(28,6) NOT NULL DEFAULT 0,
  ordered numeric(28,6) NOT NULL DEFAULT 0,
  deducted numeric(28,6) NOT NULL DEFAULT 0,
  picked numeric(28,6) NOT NULL DEFAULT 0,
  reserved_physical numeric(28,6) NOT NULL DEFAULT 0,
  reserved_ordered numeric(28,6) NOT NULL DEFAULT 0,
  on_order numeric(28,6) NOT NULL DEFAULT 0,
  currency char(3) CHECK (currency ~ '^[A-Z]{3}$'),
  last_txn_seq bigint NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, product_id, dimension_set_hash),
  FOREIGN KEY (tenant_id, dimension_set_hash) REFERENCES dimension_set (tenant_id, hash)
);
ALTER TABLE on_hand ENABLE ROW LEVEL SECURITY;
ALTER TABLE on_hand FORCE ROW LEVEL SECURITY;
CREATE POLICY on_hand_tenant_access ON on_hand AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY on_hand_legal_entity_scope ON on_hand AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The application role reads and writes through RLS; it never owns the tables.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON outbox, dimension_set, inventory_journal, inventory_journal_line, on_hand TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON inventory_transaction_origin, inventory_transaction TO passage_app';
    EXECUTE 'REVOKE DELETE, TRUNCATE ON inventory_transaction_origin, inventory_transaction FROM passage_app';
  END IF;
END
$$;
