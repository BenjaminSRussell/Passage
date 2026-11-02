-- Legal holds and erasure commands (FR-48.11, FR-48.10, AC-48.7).
-- A hold suspends erasure and retention purge for a scope inside one legal entity: a subject, a data class,
-- a single record, or any combination (every field that is set must match). Holds are released, never deleted.
-- An erasure command is the record that no hold covered the subject when the erasure fan-out event was
-- emitted; it is append-only.
CREATE TABLE IF NOT EXISTS legal_hold (
  tenant_id uuid NOT NULL,
  hold_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  subject_ref text CHECK (subject_ref IS NULL OR btrim(subject_ref) <> ''),
  data_class text CHECK (data_class IS NULL OR data_class IN ('AP_PAYEE', 'FINANCIAL_RECORD', 'EMPLOYMENT', 'CANDIDATE', 'SYSTEM_LOG')),
  record_ref text CHECK (record_ref IS NULL OR btrim(record_ref) <> ''),
  reason text NOT NULL CHECK (char_length(btrim(reason)) >= 10),
  state text NOT NULL DEFAULT 'ACTIVE' CHECK (state IN ('ACTIVE', 'RELEASED')),
  placed_by text NOT NULL,
  placed_at timestamptz NOT NULL,
  released_by text,
  released_at timestamptz,
  release_reason text,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  PRIMARY KEY (tenant_id, hold_id),
  CHECK (subject_ref IS NOT NULL OR data_class IS NOT NULL OR record_ref IS NOT NULL),
  CHECK ((state = 'RELEASED') = (released_at IS NOT NULL AND released_by IS NOT NULL AND char_length(btrim(coalesce(release_reason, ''))) >= 10))
);
CREATE INDEX IF NOT EXISTS legal_hold_active ON legal_hold (tenant_id, legal_entity_id, subject_ref) WHERE state = 'ACTIVE';
CREATE INDEX IF NOT EXISTS legal_hold_listing ON legal_hold (tenant_id, placed_at DESC, hold_id DESC);

CREATE TABLE IF NOT EXISTS erasure_command (
  tenant_id uuid NOT NULL,
  erasure_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  subject_ref text NOT NULL CHECK (btrim(subject_ref) <> ''),
  data_classes text[] NOT NULL DEFAULT '{}',
  request_id uuid,
  requested_by text NOT NULL,
  requested_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, erasure_id)
);

DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['legal_hold', 'erasure_command'] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = t AND policyname = t || '_tenant_access') THEN
      EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
      EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
      EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
      EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
    END IF;
  END LOOP;
END
$$;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON legal_hold TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON erasure_command TO passage_app';
  END IF;
END
$$;
