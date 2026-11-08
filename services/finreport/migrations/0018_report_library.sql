-- PSG-1516 (wave 15, slice 16): scheduled statement firings and the report
-- library (FR-15.8, FR-15.6, PRD §2.15.6).
--
-- report_schedule_firing is the record of one firing of a distribution schedule
-- for one ledger at one cron time. The unique key makes a firing happen once
-- however often the job runs; a firing is written when it ends (completed or
-- failed), so the table is insert-only.
--
-- report_library_entry holds the rendered output files of a scheduled run, each
-- with its SHA-256, so the library can serve exactly the bytes that were
-- distributed. Entries are written once and never changed; retain_until is the
-- retention date (REPORTING_RETENTION_CLOSED_YEARS), the sweep that deletes
-- past it is a later slice.

CREATE TABLE IF NOT EXISTS report_schedule_firing (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  schedule_code text NOT NULL,
  ledger_id uuid NOT NULL,
  legal_entity_id uuid,
  fire_time timestamptz NOT NULL,
  period_year integer NOT NULL,
  period_no integer NOT NULL,
  status text NOT NULL CHECK (status IN ('completed', 'failed')),
  attempts integer NOT NULL DEFAULT 1 CHECK (attempts >= 1),
  run_id uuid,
  error_code text NOT NULL DEFAULT '',
  error_detail text NOT NULL DEFAULT '',
  started_at timestamptz NOT NULL,
  finished_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, schedule_code, ledger_id, fire_time)
);
CREATE INDEX IF NOT EXISTS report_schedule_firing_recent
  ON report_schedule_firing (tenant_id, fire_time DESC, id DESC);
ALTER TABLE report_schedule_firing ENABLE ROW LEVEL SECURITY;
ALTER TABLE report_schedule_firing FORCE ROW LEVEL SECURITY;
CREATE POLICY report_schedule_firing_tenant_access ON report_schedule_firing AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY report_schedule_firing_legal_entity_scope ON report_schedule_firing AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS report_library_entry (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  run_id uuid NOT NULL,
  firing_id uuid NOT NULL,
  schedule_code text NOT NULL,
  definition_code text NOT NULL,
  definition_version integer NOT NULL,
  ledger_id uuid NOT NULL,
  legal_entity_id uuid,
  period_year integer NOT NULL,
  period_no integer NOT NULL,
  currency_type text NOT NULL,
  format text NOT NULL CHECK (format IN ('csv', 'html', 'xlsx', 'pdf')),
  file_name text NOT NULL,
  content_type text NOT NULL,
  size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
  sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  content bytea NOT NULL,
  retain_until timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, run_id, format)
);
CREATE INDEX IF NOT EXISTS report_library_entry_recent
  ON report_library_entry (tenant_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS report_library_entry_definition
  ON report_library_entry (tenant_id, definition_code, period_year DESC, period_no DESC);
ALTER TABLE report_library_entry ENABLE ROW LEVEL SECURITY;
ALTER TABLE report_library_entry FORCE ROW LEVEL SECURITY;
CREATE POLICY report_library_entry_tenant_access ON report_library_entry AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY report_library_entry_legal_entity_scope ON report_library_entry AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Both tables are written once (inserts use ON CONFLICT DO NOTHING) and only read after.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON report_schedule_firing, report_library_entry TO passage_app';
  END IF;
END
$$;
