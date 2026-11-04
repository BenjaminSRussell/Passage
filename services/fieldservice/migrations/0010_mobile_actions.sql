-- Field service, slice 8 (PSG-4508): the technician's append-only action log (BR-45.6,
-- FR-45.8) and the completion data it leaves on the work order (signature, notes).
-- Actions carry a client-generated id and the device timestamp; the server orders them
-- by (device_time, seq) where seq is the receipt order.
CREATE TABLE IF NOT EXISTS service_wo_action (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  work_order_id uuid NOT NULL,
  technician_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('TRAVEL_START', 'WORK_START', 'HOLD', 'RESUME', 'PART_USED', 'SIGNATURE', 'NOTE', 'COMPLETE')),
  device_time timestamptz NOT NULL,
  payload jsonb NOT NULL DEFAULT '{}',
  outcome text NOT NULL CHECK (outcome IN ('APPLIED', 'SUPERSEDED', 'REJECTED')),
  reason_code text NOT NULL DEFAULT '',
  reason text NOT NULL DEFAULT '',
  seq bigint GENERATED ALWAYS AS IDENTITY,
  received_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, work_order_id) REFERENCES service_work_order (tenant_id, id),
  FOREIGN KEY (tenant_id, technician_id) REFERENCES service_technician (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS service_wo_action_order ON service_wo_action (tenant_id, work_order_id, device_time, seq);
ALTER TABLE service_wo_action ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_wo_action FORCE ROW LEVEL SECURITY;
CREATE POLICY service_wo_action_tenant_access ON service_wo_action AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY service_wo_action_legal_entity_scope ON service_wo_action AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Completion data on the work order: the customer signature (SVC-WO-001 unless the
-- incident type exempts it) and the technician's free-text note (last writer wins by
-- device time).
ALTER TABLE service_work_order ADD COLUMN IF NOT EXISTS signature_exempt boolean NOT NULL DEFAULT false;
ALTER TABLE service_work_order ADD COLUMN IF NOT EXISTS signed_by text;
ALTER TABLE service_work_order ADD COLUMN IF NOT EXISTS signed_at timestamptz;
ALTER TABLE service_work_order ADD COLUMN IF NOT EXISTS signature_ref text;
ALTER TABLE service_work_order ADD COLUMN IF NOT EXISTS technician_note text NOT NULL DEFAULT '';
ALTER TABLE service_work_order ADD COLUMN IF NOT EXISTS note_at timestamptz;

-- Application role privileges, in the style of 0002. The log is append-only; the work
-- order keeps the privileges it has. Nothing is deleted.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON service_wo_action TO passage_app';
  END IF;
END
$$;
