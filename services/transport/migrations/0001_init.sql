-- Wave 34 slice 1: rate cards, freight bills and carrier invoices.
-- Money is numeric(20,6) with the currency alongside. Every table has
-- tenant_id first and row-level security. Commands sent with an
-- Idempotency-Key store the key and a request fingerprint on the row they
-- create, so a replay returns that row and a reused key with another body is
-- refused (idempotency_key_reuse).

CREATE TABLE IF NOT EXISTS rate_card (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  carrier_id uuid NOT NULL,
  service_id uuid NOT NULL,
  basis text NOT NULL CHECK (basis IN ('LANE', 'ZONE', 'DISTANCE', 'WEIGHT_BREAK', 'PER_PALLET', 'PER_CONTAINER', 'FLAT')),
  currency char(3) NOT NULL,
  valid_from date NOT NULL,
  valid_to date,
  minimum_charge numeric(20,6) NOT NULL DEFAULT 0 CHECK (minimum_charge >= 0),
  dim_divisor integer NOT NULL DEFAULT 5000 CHECK (dim_divisor > 0),
  fuel_pct numeric(9,6) NOT NULL DEFAULT 0 CHECK (fuel_pct >= 0),
  rate_rows jsonb NOT NULL DEFAULT '[]'::jsonb,
  accessorials jsonb NOT NULL DEFAULT '[]'::jsonb,
  idempotency_key text,
  request_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (valid_to IS NULL OR valid_to >= valid_from)
);
CREATE INDEX IF NOT EXISTS rate_card_lane ON rate_card (tenant_id, carrier_id, service_id, valid_from, valid_to);
ALTER TABLE rate_card ENABLE ROW LEVEL SECURITY;
ALTER TABLE rate_card FORCE ROW LEVEL SECURITY;
CREATE POLICY rate_card_tenant_access ON rate_card AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS freight_bill (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  load_id uuid NOT NULL,
  carrier_id uuid NOT NULL,
  currency char(3) NOT NULL,
  expected_lines jsonb NOT NULL,
  expected_total numeric(20,6) NOT NULL CHECK (expected_total >= 0),
  match_status text NOT NULL DEFAULT 'UNMATCHED'
    CHECK (match_status IN ('UNMATCHED', 'MATCHED', 'VARIANCE', 'DISPUTED', 'APPROVED', 'SETTLED')),
  carrier_invoice_id uuid,
  variance_total numeric(20,6),
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  request_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  UNIQUE (tenant_id, carrier_invoice_id)
);
CREATE INDEX IF NOT EXISTS freight_bill_status ON freight_bill (tenant_id, match_status);
CREATE INDEX IF NOT EXISTS freight_bill_created ON freight_bill (tenant_id, created_at DESC);
ALTER TABLE freight_bill ENABLE ROW LEVEL SECURITY;
ALTER TABLE freight_bill FORCE ROW LEVEL SECURITY;
CREATE POLICY freight_bill_tenant_access ON freight_bill AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS carrier_invoice (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  carrier_id uuid NOT NULL,
  invoice_no text NOT NULL CHECK (char_length(invoice_no) BETWEEN 1 AND 40),
  invoice_date date NOT NULL,
  source text NOT NULL CHECK (source IN ('EDI210', 'MANUAL', 'API')),
  freight_bill_id uuid,
  currency char(3) NOT NULL,
  lines jsonb NOT NULL,
  total numeric(20,6) NOT NULL,
  match_status text NOT NULL CHECK (match_status IN ('MATCHED', 'VARIANCE', 'UNEXPECTED_INVOICE')),
  variance_total numeric(20,6),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, carrier_id, invoice_no),
  FOREIGN KEY (tenant_id, freight_bill_id) REFERENCES freight_bill (tenant_id, id),
  CHECK ((freight_bill_id IS NULL) = (match_status = 'UNEXPECTED_INVOICE'))
);
CREATE INDEX IF NOT EXISTS carrier_invoice_status ON carrier_invoice (tenant_id, match_status, created_at DESC);
ALTER TABLE carrier_invoice ENABLE ROW LEVEL SECURITY;
ALTER TABLE carrier_invoice FORCE ROW LEVEL SECURITY;
CREATE POLICY carrier_invoice_tenant_access ON carrier_invoice AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Transactional outbox relayed by Debezium. Row-level security applies as in org.
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

-- The application role reads and writes through RLS; it never owns the tables.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON rate_card, freight_bill, carrier_invoice, outbox TO passage_app';
  END IF;
END
$$;
