-- Field service, slice 6 (PSG-4506): the agreement lifecycle (activate, suspend,
-- resume, cancel, expire; FR-45.3) and included-visit consumption. Agreements get a
-- version for If-Match, a status log records each change, and visits are recorded
-- against a line's includedVisits.
ALTER TABLE service_agreement ADD COLUMN IF NOT EXISTS version integer NOT NULL DEFAULT 1;
ALTER TABLE service_agreement ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE service_agreement ADD COLUMN IF NOT EXISTS status_reason text;

CREATE TABLE IF NOT EXISTS service_agreement_status_log (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  agreement_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  from_status text NOT NULL,
  to_status text NOT NULL,
  reason text,
  actor text NOT NULL DEFAULT '',
  at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, agreement_id) REFERENCES service_agreement (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS service_agreement_status_log_agreement ON service_agreement_status_log (tenant_id, agreement_id, at);
ALTER TABLE service_agreement_status_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_agreement_status_log FORCE ROW LEVEL SECURITY;
CREATE POLICY service_agreement_status_log_tenant_access ON service_agreement_status_log AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY service_agreement_status_log_legal_entity_scope ON service_agreement_status_log AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS service_agreement_visit (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  agreement_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  line_index integer NOT NULL CHECK (line_index >= 0),
  kind text NOT NULL CHECK (kind IN ('PREVENTIVE', 'CORRECTIVE')),
  visit_date date NOT NULL,
  work_order_id uuid,
  note text,
  -- What remained of the line's includedVisits once this visit was recorded, so a
  -- replay and the visit list answer the same as the record call.
  visits_left integer NOT NULL CHECK (visits_left >= 0),
  recorded_by text NOT NULL DEFAULT '',
  idempotency_key text,
  request_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, agreement_id) REFERENCES service_agreement (tenant_id, id)
);
-- A work order consumes at most one included visit.
CREATE UNIQUE INDEX IF NOT EXISTS service_agreement_visit_work_order ON service_agreement_visit (tenant_id, work_order_id) WHERE work_order_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS service_agreement_visit_agreement ON service_agreement_visit (tenant_id, agreement_id, line_index);
ALTER TABLE service_agreement_visit ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_agreement_visit FORCE ROW LEVEL SECURITY;
CREATE POLICY service_agreement_visit_tenant_access ON service_agreement_visit AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY service_agreement_visit_legal_entity_scope ON service_agreement_visit AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Application role privileges, in the style of 0002. The status log and visits are
-- append-only; service_agreement already has UPDATE (0002). Nothing is deleted.
-- Skipped when the role does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON service_agreement_status_log, service_agreement_visit TO passage_app';
  END IF;
END
$$;
