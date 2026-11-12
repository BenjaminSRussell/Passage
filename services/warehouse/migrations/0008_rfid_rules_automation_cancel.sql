-- #32: what an RFID read does (rules per reader), tags longer than 96 bits, and cancelling an automation order.

-- SGTIN-198, SGLN-195, GRAI-170, GDTI-174 and GIAI-202 are up to 208 bits as a reader reports them (52 hex characters).
ALTER TABLE rfid_read DROP CONSTRAINT IF EXISTS rfid_read_epc_hex_check;
ALTER TABLE rfid_read ADD CONSTRAINT rfid_read_epc_hex_check CHECK (epc_hex ~ '^[0-9A-F]{24,64}$');

-- What a rule did with a read: the action, how it went (DONE, SKIPPED or REFUSED) and why, and the receipt or work it
-- touched. A read no rule matched keeps these empty.
ALTER TABLE rfid_read ADD COLUMN IF NOT EXISTS action text NOT NULL DEFAULT '';
ALTER TABLE rfid_read ADD COLUMN IF NOT EXISTS action_outcome text NOT NULL DEFAULT '';
ALTER TABLE rfid_read ADD COLUMN IF NOT EXISTS action_detail text NOT NULL DEFAULT '' CHECK (char_length(action_detail) <= 500);
ALTER TABLE rfid_read ADD COLUMN IF NOT EXISTS action_ref text NOT NULL DEFAULT '';

-- A rule says what a read at a reader (or one antenna of it) does: RECEIVE_PALLET receives the ASN pallet whose SSCC was
-- read (a dock-door portal), CONFIRM_PUTAWAY confirms the open put-away line of that pallet whose destination is the
-- rule's location (a portal or reader at the location).
CREATE TABLE IF NOT EXISTS rfid_rule (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  reader_id text NOT NULL CHECK (char_length(reader_id) BETWEEN 1 AND 64),
  antenna integer CHECK (antenna IS NULL OR antenna BETWEEN 0 AND 255),
  action text NOT NULL CHECK (action IN ('RECEIVE_PALLET', 'CONFIRM_PUTAWAY')),
  warehouse_id text NOT NULL DEFAULT '' CHECK (char_length(warehouse_id) <= 60),
  location_code text NOT NULL DEFAULT '' CHECK (char_length(location_code) <= 60),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS rfid_rule_one_per_antenna ON rfid_rule (tenant_id, reader_id, coalesce(antenna, -1), action);

ALTER TABLE rfid_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE rfid_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY rfid_rule_tenant_access ON rfid_rule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- A cancelled order no longer holds its line: after a fault or a divert, the line can be given to the PLC again.
ALTER TABLE automation_order DROP CONSTRAINT IF EXISTS automation_order_tenant_id_work_id_line_no_key;
CREATE UNIQUE INDEX IF NOT EXISTS automation_order_live_line ON automation_order (tenant_id, work_id, line_no)
  WHERE status NOT IN ('CANCELLED', 'FAULT', 'DIVERTED', 'REFUSED');
ALTER TABLE automation_order ADD COLUMN IF NOT EXISTS cancelled_by text NOT NULL DEFAULT '';

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON rfid_rule TO passage_app';
    EXECUTE 'GRANT UPDATE ON rfid_read TO passage_app';
  END IF;
END
$$;
