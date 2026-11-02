-- Legacy delete detection (PSG-10 integration track S4). A delta over a cursor cannot see a row the legacy system
-- deleted, so a binding is read in full on a period (keycheck_hours, 0 turns it off) and the source keys that were
-- loaded earlier and are missing from the full read are recorded here as absent. Nothing is deactivated in Passage:
-- the list is for the operator. A key that comes back leaves the list at the next full read.
-- keycheck_attempt_at is stamped by every full read, failed or not, and decides when the next one is due, so a full
-- read that cannot finish (the row cap, the run deadline, the source dropping mid-read) is retried after the period
-- and the runs in between stay deltas; last_keycheck_at is the last full read that completed and keycheck_error why
-- the last one did not produce a list ('' when it did).
-- Locking: sync_state holds one row per scheduled binding. Columns with constant defaults are catalog-only; the CHECK
-- constraints scan those few rows under the ACCESS EXCLUSIVE lock this ALTER takes for an instant.
ALTER TABLE migration.sync_state
  ADD COLUMN keycheck_hours integer NOT NULL DEFAULT 24 CHECK (keycheck_hours BETWEEN 0 AND 8760),
  ADD COLUMN keycheck_attempt_at timestamptz,
  ADD COLUMN last_keycheck_at timestamptz,
  ADD COLUMN keycheck_error text NOT NULL DEFAULT '',
  ADD COLUMN absent_count integer NOT NULL DEFAULT 0 CHECK (absent_count >= 0);

CREATE TABLE migration.sync_absent (
  tenant_id uuid NOT NULL,
  binding_id uuid NOT NULL,
  source_key text NOT NULL,
  target_id text NOT NULL DEFAULT '',
  run_id uuid,
  first_absent_at timestamptz NOT NULL DEFAULT now(),
  last_checked_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, binding_id, source_key),
  FOREIGN KEY (tenant_id, binding_id) REFERENCES migration.entity_binding (tenant_id, id)
);

ALTER TABLE migration.sync_absent ENABLE ROW LEVEL SECURITY;
ALTER TABLE migration.sync_absent FORCE ROW LEVEL SECURITY;
CREATE POLICY sync_absent_tenant_access ON migration.sync_absent AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY sync_absent_legal_entity_scope ON migration.sync_absent AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*');

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    -- UPDATE: a key still absent at the next full read is re-stamped; DELETE: a key that came back leaves the list.
    GRANT SELECT, INSERT, UPDATE, DELETE ON migration.sync_absent TO passage_app;
  END IF;
END
$$;
