-- #32 (32-4, 32-5): reads of RFID tags posted by readers and portals, and the automation orders the warehouse's transport
-- work sends to a PLC (an AS/RS, a conveyor or a sorter) with the events the PLC sends back.

CREATE TABLE IF NOT EXISTS rfid_read (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  reader_id text NOT NULL CHECK (char_length(reader_id) BETWEEN 1 AND 64),
  antenna integer NOT NULL DEFAULT 0 CHECK (antenna BETWEEN 0 AND 255),
  location_code text NOT NULL DEFAULT '' CHECK (char_length(location_code) <= 60),
  epc_hex text NOT NULL CHECK (epc_hex ~ '^[0-9A-F]{24}$'),
  scheme text NOT NULL,
  gs1_key text NOT NULL,
  serial text NOT NULL DEFAULT '',
  rssi integer CHECK (rssi IS NULL OR rssi BETWEEN -200 AND 0),
  read_at timestamptz NOT NULL,
  received_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS rfid_read_key ON rfid_read (tenant_id, gs1_key, read_at DESC);
CREATE INDEX IF NOT EXISTS rfid_read_dedupe ON rfid_read (tenant_id, reader_id, epc_hex, read_at DESC);

-- One order per transport work line: the move the PLC is asked to make, and how it went.
CREATE TABLE IF NOT EXISTS automation_order (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  work_id uuid NOT NULL,
  line_no integer NOT NULL,
  station text NOT NULL CHECK (char_length(station) BETWEEN 1 AND 8),
  load_unit text NOT NULL CHECK (load_unit ~ '^[0-9]{18}$'),
  from_location text NOT NULL CHECK (char_length(from_location) BETWEEN 1 AND 12),
  to_location text NOT NULL CHECK (char_length(to_location) BETWEEN 1 AND 12),
  priority smallint NOT NULL CHECK (priority BETWEEN 1 AND 9),
  status text NOT NULL CHECK (status IN ('PENDING', 'SENT', 'REFUSED', 'ARRIVED', 'DIVERTED', 'FAULT', 'CANCELLED')),
  reason text NOT NULL DEFAULT '',
  confirm_error text NOT NULL DEFAULT '',
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  sent_at timestamptz,
  done_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, work_id, line_no),
  FOREIGN KEY (tenant_id, work_id, line_no) REFERENCES work_line (tenant_id, work_id, line_no)
);
CREATE INDEX IF NOT EXISTS automation_order_pending ON automation_order (tenant_id, station, status, created_at);
CREATE INDEX IF NOT EXISTS automation_order_unit ON automation_order (tenant_id, station, load_unit) WHERE status = 'SENT';

-- What the PLC reported. A telegram's sequence number is only unique within one link session (it restarts at 1 when
-- the connector dials the PLC again, and wraps after 99999), so a repeat is the same (station, session, seq) received
-- within a short window (store.EventDedupeWindow); the same number in another session, or long after, is a new event.
CREATE TABLE IF NOT EXISTS automation_event (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  station text NOT NULL CHECK (char_length(station) BETWEEN 1 AND 8),
  session text NOT NULL CHECK (char_length(session) BETWEEN 1 AND 64),
  seq integer NOT NULL CHECK (seq >= 0),
  kind text NOT NULL CHECK (kind IN ('arrived', 'diverted', 'fault')),
  load_unit text NOT NULL DEFAULT '',
  location text NOT NULL DEFAULT '',
  lane text NOT NULL DEFAULT '',
  code text NOT NULL DEFAULT '',
  text text NOT NULL DEFAULT '',
  at timestamptz NOT NULL,
  received_at timestamptz NOT NULL DEFAULT now(),
  order_id uuid,
  PRIMARY KEY (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS automation_event_seq ON automation_event (tenant_id, station, session, seq, received_at DESC);

ALTER TABLE rfid_read ENABLE ROW LEVEL SECURITY;
ALTER TABLE rfid_read FORCE ROW LEVEL SECURITY;
CREATE POLICY rfid_read_tenant_access ON rfid_read AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
ALTER TABLE automation_order ENABLE ROW LEVEL SECURITY;
ALTER TABLE automation_order FORCE ROW LEVEL SECURITY;
CREATE POLICY automation_order_tenant_access ON automation_order AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
ALTER TABLE automation_event ENABLE ROW LEVEL SECURITY;
ALTER TABLE automation_event FORCE ROW LEVEL SECURITY;
CREATE POLICY automation_event_tenant_access ON automation_event AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON rfid_read, automation_event TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON automation_order TO passage_app';
  END IF;
END
$$;
