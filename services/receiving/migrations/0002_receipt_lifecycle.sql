CREATE TABLE IF NOT EXISTS receiving_number_counter (
  tenant_id uuid NOT NULL,
  prefix text NOT NULL,
  next_value bigint NOT NULL DEFAULT 1 CHECK (next_value > 0),
  PRIMARY KEY (tenant_id, prefix)
);

ALTER TABLE receiving_receipt
  ADD COLUMN IF NOT EXISTS receipt_number text,
  ADD COLUMN IF NOT EXISTS carrier text,
  ADD COLUMN IF NOT EXISTS pallet_count int,
  ADD COLUMN IF NOT EXISTS posting_date date,
  ADD COLUMN IF NOT EXISTS ledger_entry_id text,
  ADD COLUMN IF NOT EXISTS currency char(3),
  ADD COLUMN IF NOT EXISTS exchange_rate numeric(18,8) NOT NULL DEFAULT 1,
  ADD COLUMN IF NOT EXISTS corrects_receipt_id uuid,
  ADD COLUMN IF NOT EXISTS posted_at timestamptz,
  ADD COLUMN IF NOT EXISTS updated_at timestamptz;

CREATE UNIQUE INDEX IF NOT EXISTS receiving_receipt_number_uq ON receiving_receipt (tenant_id, receipt_number);

ALTER TABLE receiving_receipt_line
  ADD COLUMN IF NOT EXISTS po_line_id uuid,
  ADD COLUMN IF NOT EXISTS unit text,
  ADD COLUMN IF NOT EXISTS location_id uuid,
  ADD COLUMN IF NOT EXISTS inventory_status text NOT NULL DEFAULT 'AVAILABLE' CONSTRAINT receiving_receipt_line_inventory_status_ck CHECK (inventory_status IN ('AVAILABLE', 'QUARANTINE', 'BLOCKED')),
  ADD COLUMN IF NOT EXISTS amount numeric(20,6),
  ADD COLUMN IF NOT EXISTS amount_acct numeric(20,6),
  ADD COLUMN IF NOT EXISTS ordered_remaining numeric(20,6),
  ADD COLUMN IF NOT EXISTS over_delivery_pct numeric(9,4),
  ADD COLUMN IF NOT EXISTS batch_tracked boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS serial_tracked boolean NOT NULL DEFAULT false;

ALTER TABLE receiving_grir_open_item
  ADD COLUMN IF NOT EXISTS vendor_id uuid,
  ADD COLUMN IF NOT EXISTS po_line_id uuid,
  ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'OPEN' CONSTRAINT receiving_grir_open_item_status_ck CHECK (status IN ('OPEN', 'PARTIAL', 'CLEARED', 'WRITTEN_OFF')),
  ADD COLUMN IF NOT EXISTS cleared_at timestamptz;

CREATE INDEX IF NOT EXISTS receiving_grir_po_line_status_ix ON receiving_grir_open_item (tenant_id, po_line_id, status);
CREATE INDEX IF NOT EXISTS receiving_grir_vendor_status_ix ON receiving_grir_open_item (tenant_id, vendor_id, status);

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = 'receiving_number_counter' AND policyname = 'receiving_number_counter_tenant_access') THEN
    ALTER TABLE receiving_number_counter ENABLE ROW LEVEL SECURITY;
    ALTER TABLE receiving_number_counter FORCE ROW LEVEL SECURITY;
    CREATE POLICY receiving_number_counter_tenant_access ON receiving_number_counter AS PERMISSIVE USING (tenant_id = current_setting('passage.tenant_id', true)::uuid) WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
  END IF;
END
$$;

GRANT SELECT, INSERT, UPDATE, DELETE ON receiving_number_counter TO passage_app;
