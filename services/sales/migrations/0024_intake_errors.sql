-- Wave 27 slice 20: the intake error log (PRD 5.27.6 and 5.27.14, "Intake
-- errors"). A channel order refused by POST /orders (validation, a listing
-- denial, a payload that differs from an earlier one, ...) rolls back with the
-- request, so the refusal is written in its own transaction to this table.
-- One row per (legal entity, channel, external reference, payload hash, error
-- code): a retry of the same failing payload raises attempts and last_seen_at
-- and reopens a RESOLVED row instead of adding another. The payload itself is
-- never stored, only its hash. external_ref is '' when the request had none.

CREATE TABLE IF NOT EXISTS intake_error (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  channel text NOT NULL,
  external_ref text NOT NULL DEFAULT '',
  request_hash text NOT NULL,
  error_code text NOT NULL,
  message text NOT NULL,
  status text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'RESOLVED')),
  attempts int NOT NULL DEFAULT 1 CHECK (attempts >= 1),
  first_seen_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  resolved_by text,
  resolved_at timestamptz,
  resolution_note text,
  version int NOT NULL DEFAULT 1,
  PRIMARY KEY (tenant_id, id),
  CHECK ((status = 'RESOLVED') = (resolved_at IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS intake_error_key ON intake_error
  (tenant_id, legal_entity_id, channel, external_ref, request_hash, error_code);
CREATE INDEX IF NOT EXISTS intake_error_list ON intake_error (tenant_id, status, last_seen_at DESC);

ALTER TABLE intake_error ENABLE ROW LEVEL SECURITY;
ALTER TABLE intake_error FORCE ROW LEVEL SECURITY;
CREATE POLICY intake_error_tenant_access ON intake_error AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY intake_error_legal_entity_scope ON intake_error AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The store inserts, upserts (update on a repeat) and resolves; it never deletes.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON intake_error TO passage_app';
  END IF;
END
$$;
