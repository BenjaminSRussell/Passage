-- Wave 37 slice 1: asset register, depreciation books, the append-only asset
-- transaction log, IFRS 16 leases with their schedule, the outbox and the
-- idempotency store. Every table carries tenant_id first and has ENABLE and
-- FORCE row-level security with the dbx.RLSPolicy template; tables with
-- legal_entity_id also carry the dbx.LegalEntityScopePolicy.

CREATE TABLE IF NOT EXISTS asset (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  asset_number text NOT NULL CHECK (char_length(asset_number) BETWEEN 1 AND 40),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  class_code text NOT NULL CHECK (class_code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  status text NOT NULL DEFAULT 'NotYetAcquired' CHECK (status IN ('NotYetAcquired', 'Open', 'Sold', 'Scrapped', 'Transferred')),
  barcode text CHECK (barcode IS NULL OR char_length(barcode) BETWEEN 1 AND 64),
  quantity numeric(19,4) NOT NULL DEFAULT 1 CHECK (quantity > 0),
  acquired_date date,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT asset_number_unique UNIQUE (tenant_id, legal_entity_id, asset_number),
  CONSTRAINT asset_barcode_unique UNIQUE (tenant_id, legal_entity_id, barcode)
);
CREATE INDEX IF NOT EXISTS asset_status ON asset (tenant_id, status, asset_number);
ALTER TABLE asset ENABLE ROW LEVEL SECURITY;
ALTER TABLE asset FORCE ROW LEVEL SECURITY;
CREATE POLICY asset_tenant_access ON asset AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY asset_legal_entity_scope ON asset AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS asset_book (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  asset_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  book_code text NOT NULL CHECK (book_code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  currency char(3) NOT NULL,
  method text NOT NULL CHECK (method IN ('StraightLine', 'DecliningBalance')),
  factor numeric(9,6) NOT NULL DEFAULT 0 CHECK (factor >= 0),
  switch_to_straight_line boolean NOT NULL DEFAULT false,
  service_life_periods integer NOT NULL CHECK (service_life_periods BETWEEN 1 AND 1200),
  salvage_value numeric(20,6) NOT NULL DEFAULT 0 CHECK (salvage_value >= 0),
  convention text NOT NULL CHECK (convention IN ('None', 'FullMonth', 'MidMonth15', 'HalfYear')),
  in_service_date date NOT NULL,
  -- cost, accumulated_depreciation and nbv are projections of asset_transaction.
  cost numeric(20,6) NOT NULL CHECK (cost >= 0),
  accumulated_depreciation numeric(20,6) NOT NULL DEFAULT 0 CHECK (accumulated_depreciation >= 0),
  nbv numeric(20,6) NOT NULL,
  status text NOT NULL DEFAULT 'Open' CHECK (status IN ('Open', 'Sold', 'Scrapped')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT asset_book_code_unique UNIQUE (tenant_id, asset_id, book_code),
  FOREIGN KEY (tenant_id, asset_id) REFERENCES asset (tenant_id, id),
  CHECK (nbv = cost - accumulated_depreciation)
);
CREATE INDEX IF NOT EXISTS asset_book_asset ON asset_book (tenant_id, asset_id);
CREATE INDEX IF NOT EXISTS asset_book_status ON asset_book (tenant_id, legal_entity_id, status);
ALTER TABLE asset_book ENABLE ROW LEVEL SECURITY;
ALTER TABLE asset_book FORCE ROW LEVEL SECURITY;
CREATE POLICY asset_book_tenant_access ON asset_book AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY asset_book_legal_entity_scope ON asset_book AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS asset_transaction (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  asset_book_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  type text NOT NULL CHECK (type IN ('Acquisition', 'Depreciation', 'DisposalSale', 'DisposalScrap')),
  transaction_date date NOT NULL,
  -- Signed amounts: acquisitions add cost, disposals carry negative cost and
  -- negative accumulated depreciation (the reversal of the disposed share).
  cost_amount numeric(20,6) NOT NULL DEFAULT 0,
  accumulated_amount numeric(20,6) NOT NULL DEFAULT 0,
  proceeds_amount numeric(20,6),
  gain_loss numeric(20,6),
  journal_entry_id uuid,
  source_type text NOT NULL DEFAULT 'manual',
  source_id uuid,
  reversal_of_id uuid,
  run_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, asset_book_id) REFERENCES asset_book (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS asset_transaction_book_date ON asset_transaction (tenant_id, asset_book_id, transaction_date);
CREATE INDEX IF NOT EXISTS asset_transaction_run ON asset_transaction (tenant_id, run_id) WHERE run_id IS NOT NULL;
ALTER TABLE asset_transaction ENABLE ROW LEVEL SECURITY;
ALTER TABLE asset_transaction FORCE ROW LEVEL SECURITY;
CREATE POLICY asset_transaction_tenant_access ON asset_transaction AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY asset_transaction_legal_entity_scope ON asset_transaction AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
-- AssetTransaction is append-only: corrections are new rows (reversal_of_id), never edits.
CREATE OR REPLACE FUNCTION asset_transaction_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'asset_transaction is append-only' USING ERRCODE = 'check_violation';
END
$$;
CREATE OR REPLACE TRIGGER asset_transaction_no_update BEFORE UPDATE OR DELETE ON asset_transaction
  FOR EACH ROW EXECUTE FUNCTION asset_transaction_append_only();

CREATE TABLE IF NOT EXISTS lease (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  lease_number text NOT NULL CHECK (char_length(lease_number) BETWEEN 1 AND 40),
  commencement_date date NOT NULL,
  term_months integer NOT NULL CHECK (term_months BETWEEN 1 AND 600),
  payments_per_year integer NOT NULL CHECK (payments_per_year IN (1, 2, 4, 12)),
  payment_amount numeric(20,6) NOT NULL CHECK (payment_amount > 0),
  payment_timing text NOT NULL CHECK (payment_timing IN ('arrears', 'advance')),
  ibr_rate numeric(9,6) NOT NULL CHECK (ibr_rate > 0 AND ibr_rate <= 0.5),
  currency char(3) NOT NULL,
  liability numeric(20,6) NOT NULL,
  rou_asset numeric(20,6) NOT NULL,
  status text NOT NULL DEFAULT 'Active' CHECK (status IN ('Draft', 'Active', 'Modified', 'Terminated')),
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT lease_number_unique UNIQUE (tenant_id, legal_entity_id, lease_number)
);
ALTER TABLE lease ENABLE ROW LEVEL SECURITY;
ALTER TABLE lease FORCE ROW LEVEL SECURITY;
CREATE POLICY lease_tenant_access ON lease AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY lease_legal_entity_scope ON lease AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS lease_schedule_line (
  tenant_id uuid NOT NULL,
  lease_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  period_no integer NOT NULL CHECK (period_no >= 1),
  opening_liability numeric(20,6) NOT NULL,
  interest numeric(20,6) NOT NULL,
  payment numeric(20,6) NOT NULL,
  closing_liability numeric(20,6) NOT NULL,
  rou_depreciation numeric(20,6) NOT NULL,
  posted_journal_id uuid,
  PRIMARY KEY (tenant_id, lease_id, period_no),
  FOREIGN KEY (tenant_id, lease_id) REFERENCES lease (tenant_id, id)
);
ALTER TABLE lease_schedule_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE lease_schedule_line FORCE ROW LEVEL SECURITY;
CREATE POLICY lease_schedule_line_tenant_access ON lease_schedule_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY lease_schedule_line_legal_entity_scope ON lease_schedule_line AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- dbx.OutboxDDL
CREATE TABLE IF NOT EXISTS outbox (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL,
  aggregate_type text NOT NULL,
  aggregate_id text NOT NULL,
  type text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE outbox REPLICA IDENTITY FULL;
ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY outbox_tenant_access ON outbox AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- dbx.IdempotencyDDL: every command stores its request hash and response here
-- in the same transaction as the change, so a retry replays the response.
CREATE TABLE IF NOT EXISTS idempotency_keys (
  tenant_id uuid NOT NULL,
  key text NOT NULL,
  request_hash text NOT NULL,
  response_status int NOT NULL,
  response_body jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, key)
);
ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY idempotency_keys_tenant_access ON idempotency_keys AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
