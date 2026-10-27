-- Wave 16 slice 2: blocking and quarantine (FR-16.8) and cycle counting (FR-16.9).
-- Widens two CHECK lists and adds five tables.

ALTER TABLE inventory_journal DROP CONSTRAINT IF EXISTS inventory_journal_journal_type_check;
ALTER TABLE inventory_journal ADD CONSTRAINT inventory_journal_journal_type_check
  CHECK (journal_type IN ('MOVEMENT', 'ADJUSTMENT', 'OPENING_BALANCE', 'COUNT'));

-- BLOCKING origins are the legs of a blocking or its release; reference_number
-- holds the blocking id.
ALTER TABLE inventory_transaction_origin DROP CONSTRAINT IF EXISTS inventory_transaction_origin_source_type_check;
ALTER TABLE inventory_transaction_origin ADD CONSTRAINT inventory_transaction_origin_source_type_check
  CHECK (source_type IN ('INVENT_JOURNAL_LINE', 'PURCH_LINE', 'RECEIPT_LINE', 'SALES_LINE', 'PROD_LINE', 'COUNT_LINE', 'TRANSFER_LINE', 'OPENING', 'BLOCKING'));
CREATE INDEX IF NOT EXISTS inventory_transaction_origin_reference_idx
  ON inventory_transaction_origin (tenant_id, reference_number) WHERE reference_number IS NOT NULL;

-- A blocking moves quantity from unrestricted stock (source dimension set, no
-- inventory status) to the same dimensions with inventory_status BLOCKED or
-- QUARANTINE, and back on release. Each move is a valued issue / receipt pair.
CREATE TABLE IF NOT EXISTS inventory_blocking (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  id uuid NOT NULL,
  idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 255),
  product_id uuid NOT NULL,
  dimension_set_hash bytea NOT NULL,
  blocked_dimension_set_hash bytea NOT NULL,
  qty numeric(28,6) NOT NULL CHECK (qty > 0),
  unit text NOT NULL CHECK (unit ~ '^[A-Z0-9_-]{1,12}$'),
  target_status text NOT NULL CHECK (target_status IN ('BLOCKED', 'QUARANTINE')),
  reason text NOT NULL CHECK (reason IN ('QUALITY', 'DAMAGE', 'RECALL', 'LEGAL', 'OTHER')),
  note text CHECK (note IS NULL OR char_length(note) <= 500),
  state text NOT NULL CHECK (state IN ('ACTIVE', 'RELEASED')),
  disposition text CHECK (disposition IN ('RELEASE', 'SCRAP', 'RETURN')),
  physical_date date NOT NULL,
  version integer NOT NULL DEFAULT 1,
  created_by text,
  created_at timestamptz NOT NULL DEFAULT now(),
  released_by text,
  released_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, legal_entity_id, product_id, dimension_set_hash)
    REFERENCES on_hand (tenant_id, legal_entity_id, product_id, dimension_set_hash),
  FOREIGN KEY (tenant_id, legal_entity_id, product_id, blocked_dimension_set_hash)
    REFERENCES on_hand (tenant_id, legal_entity_id, product_id, dimension_set_hash),
  CHECK ((state = 'RELEASED') = (released_at IS NOT NULL AND disposition IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS inventory_blocking_list_idx ON inventory_blocking (tenant_id, state, created_at DESC, id DESC);
ALTER TABLE inventory_blocking ENABLE ROW LEVEL SECURITY;
ALTER TABLE inventory_blocking FORCE ROW LEVEL SECURITY;
CREATE POLICY inventory_blocking_tenant_access ON inventory_blocking AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY inventory_blocking_legal_entity_scope ON inventory_blocking AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- A count plan selects the stock of one legal entity, optionally one warehouse
-- (the warehouse dimension value), and carries the FR-16.9 tolerances.
CREATE TABLE IF NOT EXISTS count_plan (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  id uuid NOT NULL,
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  warehouse text CHECK (warehouse IS NULL OR char_length(warehouse) BETWEEN 1 AND 60),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  unit text NOT NULL CHECK (unit ~ '^[A-Z0-9_-]{1,12}$'),
  tolerance_pct numeric(9,6) NOT NULL CHECK (tolerance_pct BETWEEN 0 AND 100),
  tolerance_value numeric(20,6) NOT NULL CHECK (tolerance_value >= 0),
  blind boolean NOT NULL DEFAULT false,
  idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 255),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS count_plan_list_idx ON count_plan (tenant_id, created_at DESC, id DESC);
ALTER TABLE count_plan ENABLE ROW LEVEL SECURITY;
ALTER TABLE count_plan FORCE ROW LEVEL SECURITY;
CREATE POLICY count_plan_tenant_access ON count_plan AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY count_plan_legal_entity_scope ON count_plan AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS count_sheet (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  id uuid NOT NULL,
  plan_id uuid NOT NULL,
  number text NOT NULL,
  status text NOT NULL CHECK (status IN ('OPEN', 'POSTED')),
  idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 255),
  journal_id uuid,
  approved_by text,
  posted_by text,
  posted_at timestamptz,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, plan_id) REFERENCES count_plan (tenant_id, id),
  FOREIGN KEY (tenant_id, journal_id) REFERENCES inventory_journal (tenant_id, id),
  CHECK ((status = 'POSTED') = (posted_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS count_sheet_list_idx ON count_sheet (tenant_id, created_at DESC, id DESC);
ALTER TABLE count_sheet ENABLE ROW LEVEL SECURITY;
ALTER TABLE count_sheet FORCE ROW LEVEL SECURITY;
CREATE POLICY count_sheet_tenant_access ON count_sheet AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY count_sheet_legal_entity_scope ON count_sheet AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS count_sheet_line (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  sheet_id uuid NOT NULL,
  line_no integer NOT NULL CHECK (line_no > 0),
  product_id uuid NOT NULL,
  dimension_set_hash bytea NOT NULL,
  expected_qty numeric(28,6) NOT NULL,
  counted_qty numeric(28,6) CHECK (counted_qty IS NULL OR counted_qty >= 0),
  counted_by text,
  counted_at timestamptz,
  posted_variance numeric(28,6),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, sheet_id, line_no),
  FOREIGN KEY (tenant_id, sheet_id) REFERENCES count_sheet (tenant_id, id),
  FOREIGN KEY (tenant_id, dimension_set_hash) REFERENCES dimension_set (tenant_id, hash)
);
ALTER TABLE count_sheet_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE count_sheet_line FORCE ROW LEVEL SECURITY;
CREATE POLICY count_sheet_line_tenant_access ON count_sheet_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON inventory_blocking, count_plan, count_sheet, count_sheet_line TO passage_app';
  END IF;
END
$$;
