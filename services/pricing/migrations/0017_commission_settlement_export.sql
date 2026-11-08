-- Pricing context, wave 26 slice 24: the record of a commission settlement export (FR-26.18,
-- PRD 5.26.15). An export hands one legal entity's commission for a period of invoice dates to
-- AP (w19) or payroll (w43). Recording it freezes the lines it sent (the CSV can be fetched
-- again unchanged) and stops the same period from being sent twice to the same target until
-- the export is voided. Additive only.
CREATE TABLE IF NOT EXISTS commission_settlement_export (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  target text NOT NULL CHECK (target IN ('AP', 'PAYROLL')),
  period_from date NOT NULL,
  period_to date NOT NULL,
  status text NOT NULL DEFAULT 'ISSUED' CHECK (status IN ('ISSUED', 'VOIDED')),
  -- The rows of the export: [{"repId", "currency", "amount", "transactions"}], ordered by rep then currency.
  lines jsonb NOT NULL CHECK (jsonb_typeof(lines) = 'array'),
  line_count integer NOT NULL CHECK (line_count >= 1),
  csv_sha256 text NOT NULL CHECK (csv_sha256 ~ '^[0-9a-f]{64}$'),
  exported_by text NOT NULL DEFAULT '' CHECK (length(exported_by) <= 128),
  void_reason text NOT NULL DEFAULT '' CHECK (length(void_reason) <= 500),
  voided_by text NOT NULL DEFAULT '' CHECK (length(voided_by) <= 128),
  voided_at timestamptz,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (period_to >= period_from)
);
CREATE INDEX IF NOT EXISTS commission_settlement_export_period_ix
  ON commission_settlement_export (tenant_id, legal_entity_id, target, period_from, period_to);

ALTER TABLE commission_settlement_export ENABLE ROW LEVEL SECURITY;
ALTER TABLE commission_settlement_export FORCE ROW LEVEL SECURITY;
CREATE POLICY commission_settlement_export_tenant_access ON commission_settlement_export AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY commission_settlement_export_legal_entity_scope ON commission_settlement_export AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The store reads, inserts, and updates (void, with a row lock). Never deletes. Skipped when
-- the role does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON commission_settlement_export TO passage_app';
  END IF;
END
$$;
