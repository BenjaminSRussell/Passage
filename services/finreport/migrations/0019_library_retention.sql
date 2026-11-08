-- PSG-1517 (wave 15, slice 17): report library retention and legal hold
-- (FR-15.8, PRD §2.15.9 `library-retention-sweep`, §2.15.15).
--
-- report_legal_hold is a hold on the report library of one legal entity: while
-- an active hold (released_at IS NULL) exists the retention sweep deletes none
-- of that entity's files. One active hold per entity; releasing sets
-- released_at, so the table keeps every hold ever placed. revision is the
-- optimistic version a release is checked against.
--
-- report_library_sweep records one pass of the sweep over one legal entity
-- (insert-only): how many files past their retain_until were deleted, how many
-- a hold kept. The audit trail and library.swept.v1 carry the same facts.
--
-- A file is deleted only by the sweep, only after retain_until and never under
-- a hold: the trigger below refuses any other delete, so the guarantee does not
-- depend on the application code alone.

CREATE TABLE IF NOT EXISTS report_legal_hold (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  reason text NOT NULL CHECK (reason <> '' AND char_length(reason) <= 500),
  placed_by text NOT NULL CHECK (placed_by <> ''),
  placed_at timestamptz NOT NULL DEFAULT now(),
  released_by text,
  released_at timestamptz,
  release_note text NOT NULL DEFAULT '',
  revision integer NOT NULL DEFAULT 1 CHECK (revision >= 1),
  idempotency_key text NOT NULL CHECK (idempotency_key <> ''),
  request_hash text NOT NULL,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK ((released_at IS NULL) = (released_by IS NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS report_legal_hold_active
  ON report_legal_hold (tenant_id, legal_entity_id) WHERE released_at IS NULL;
CREATE INDEX IF NOT EXISTS report_legal_hold_recent
  ON report_legal_hold (tenant_id, placed_at DESC, id DESC);
ALTER TABLE report_legal_hold ENABLE ROW LEVEL SECURITY;
ALTER TABLE report_legal_hold FORCE ROW LEVEL SECURITY;
CREATE POLICY report_legal_hold_tenant_access ON report_legal_hold AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY report_legal_hold_legal_entity_scope ON report_legal_hold AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS report_library_sweep (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  swept_at timestamptz NOT NULL DEFAULT now(),
  cutoff timestamptz NOT NULL,
  deleted_count integer NOT NULL CHECK (deleted_count >= 0),
  deleted_bytes bigint NOT NULL CHECK (deleted_bytes >= 0),
  held_count integer NOT NULL CHECK (held_count >= 0),
  hold_id uuid,
  actor text NOT NULL CHECK (actor <> ''),
  PRIMARY KEY (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS report_library_sweep_recent
  ON report_library_sweep (tenant_id, swept_at DESC, id DESC);
ALTER TABLE report_library_sweep ENABLE ROW LEVEL SECURITY;
ALTER TABLE report_library_sweep FORCE ROW LEVEL SECURITY;
CREATE POLICY report_library_sweep_tenant_access ON report_library_sweep AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY report_library_sweep_legal_entity_scope ON report_library_sweep AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The audit trail records holds and sweeps (the kind list is a CHECK, so it is replaced).
ALTER TABLE audit_trail_entry DROP CONSTRAINT IF EXISTS audit_trail_entry_kind_check;
ALTER TABLE audit_trail_entry ADD CONSTRAINT audit_trail_entry_kind_check CHECK (kind IN (
    'posted', 'replayed', 'reversed', 'batch_submitted', 'batch_approved', 'batch_rejected',
    'period_changed', 'reopen_approved', 'rate_override', 'tax_fallback', 'allocation_run',
    'year_end_run', 'rebuild',
    'statement_run', 'definition_published', 'definition_retired', 'hierarchy_published',
    'schedule_saved', 'audit_exported',
    'legal_hold_placed', 'legal_hold_released', 'library_swept'));

-- Hold placement and the delete of a held entity's files are serialised per
-- tenant and legal entity by a transaction-scoped advisory lock: a hold being
-- placed holds the lock until it commits, and a delete waits for it and then
-- sees the committed hold (READ COMMITTED gives each statement of the guard a
-- fresh snapshot). Without it a sweep that read "no hold" could delete files
-- after a concurrent hold had committed and been confirmed to the caller.
CREATE OR REPLACE FUNCTION report_legal_hold_lock(p_tenant uuid, p_entity uuid) RETURNS void AS $$
BEGIN
  PERFORM pg_advisory_xact_lock(hashtextextended('finreport.report_legal_hold:' || p_tenant::text || ':' || p_entity::text, 0));
END
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION report_legal_hold_take_lock() RETURNS trigger AS $$
BEGIN
  PERFORM report_legal_hold_lock(NEW.tenant_id, NEW.legal_entity_id);
  RETURN NEW;
END
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS report_legal_hold_take_lock ON report_legal_hold;
CREATE TRIGGER report_legal_hold_take_lock BEFORE INSERT OR UPDATE ON report_legal_hold
  FOR EACH ROW EXECUTE FUNCTION report_legal_hold_take_lock();

-- Backstop for the retention rule: a stored file is deleted only after its
-- retention date and never while its legal entity is under an active hold.
CREATE OR REPLACE FUNCTION report_library_entry_guard_delete() RETURNS trigger AS $$
BEGIN
  IF OLD.retain_until > now() THEN
    RAISE EXCEPTION 'report library file % is retained until %', OLD.id, OLD.retain_until
      USING ERRCODE = 'restrict_violation';
  END IF;
  IF OLD.legal_entity_id IS NULL THEN
    RAISE EXCEPTION 'report library file % has no legal entity, so no legal hold could cover it', OLD.id
      USING ERRCODE = 'restrict_violation';
  END IF;
  PERFORM report_legal_hold_lock(OLD.tenant_id, OLD.legal_entity_id);
  IF EXISTS (
      SELECT 1 FROM report_legal_hold h
      WHERE h.tenant_id = OLD.tenant_id AND h.legal_entity_id = OLD.legal_entity_id AND h.released_at IS NULL) THEN
    RAISE EXCEPTION 'report library file % is under legal hold', OLD.id
      USING ERRCODE = 'restrict_violation';
  END IF;
  RETURN OLD;
END
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS report_library_entry_guard_delete ON report_library_entry;
CREATE TRIGGER report_library_entry_guard_delete BEFORE DELETE ON report_library_entry
  FOR EACH ROW EXECUTE FUNCTION report_library_entry_guard_delete();

-- The holds are inserted and released (UPDATE of the release columns only, so
-- the application role cannot move a hold to another entity or rewrite its
-- reason); the sweep log is insert-only; the sweep deletes library files past
-- retention, which the application role could not do before this migration.
-- It still has no UPDATE and no TRUNCATE on the library (TRUNCATE fires no row
-- trigger), so retain_until cannot be moved and the guard cannot be skipped.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON report_legal_hold TO passage_app';
    EXECUTE 'GRANT UPDATE (released_at, released_by, release_note, revision) ON report_legal_hold TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON report_library_sweep TO passage_app';
    EXECUTE 'GRANT DELETE ON report_library_entry TO passage_app';
  END IF;
END
$$;
