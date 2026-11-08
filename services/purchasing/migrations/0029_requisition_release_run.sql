-- Wave 17 purchasing slice 28: the requisition release run (FR-17.3, PRD 3.17.16 "Release approved requisitions").
-- A run takes the OPEN lines of APPROVED requisitions of one legal entity that have a vendor, consolidates them into
-- draft POs and records what it ordered and what it left in the queue (and why). The record is the buyer's report of
-- the run; it is written once and never changed.
CREATE TABLE IF NOT EXISTS purchase_release_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  window_days integer NOT NULL CHECK (window_days BETWEEN 1 AND 60),
  max_lines integer NOT NULL CHECK (max_lines BETWEEN 1 AND 500),
  status text NOT NULL CHECK (status IN ('EMPTY', 'COMPLETED', 'PARTIAL', 'FAILED')),
  lines_considered integer NOT NULL CHECK (lines_considered >= 0),
  lines_released integer NOT NULL CHECK (lines_released >= 0),
  lines_skipped integer NOT NULL CHECK (lines_skipped >= 0),
  orders jsonb NOT NULL DEFAULT '[]'::jsonb,
  skipped jsonb NOT NULL DEFAULT '[]'::jsonb,
  warnings jsonb NOT NULL DEFAULT '[]'::jsonb,
  requested_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK (lines_released + lines_skipped = lines_considered)
);
CREATE INDEX IF NOT EXISTS purchase_release_run_list ON purchase_release_run (tenant_id, id DESC);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['purchase_release_run']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)',
                   t || '_tenant_access', t);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '',''))) WITH CHECK (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))',
                   t || '_legal_entity_scope', t);
  END LOOP;
END $$;

-- passage_app: a run is written once and read; it is never updated or deleted.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON purchase_release_run TO passage_app';
  END IF;
END
$$;
