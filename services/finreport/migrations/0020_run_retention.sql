-- PSG-1518 (wave 15, slice 18): retention of ad-hoc statement runs
-- (FR-15.8, PRD §2.15.13 `REPORTING_RETENTION_ADHOC_DAYS`, §2.15.14
-- `reporting.retention_adhoc_days`: 90 days by default, 7 to 730).
--
-- An ad-hoc run is a statement_run that no schedule firing and no report
-- library file refers to. The weekly sweep (`finreport sweep-library`) deletes
-- such runs, with their cells, once they are older than the retention days,
-- except when a legal entity of the run is under legal hold (migration 0019).
-- Runs of a schedule stay while the firing record or the library files exist.
--
-- report_run_sweep records one pass of the sweep over the ad-hoc runs of one
-- legal entity (insert-only): how many runs and cells were deleted, how many
-- runs a hold kept. The audit trail and runs.swept.v1 carry the same facts.
--
-- The guard trigger below is the backstop: a run is deleted only when it is
-- older than 7 days (the lowest retention the configuration allows), is not a
-- scheduled run, has a legal entity, and none of its legal entities is under
-- hold, whoever issues the delete. The application role gets DELETE on
-- statement_run only, and loses UPDATE of every statement_run column but
-- duration_ms, so it cannot age a run or move it out of a held entity to get
-- past the guard. The cells go with their run through ON DELETE CASCADE,
-- which runs as the table owner, so the role has no DELETE on statement_cell
-- and cannot take cells away from a run that still exists.

CREATE TABLE IF NOT EXISTS report_run_sweep (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  swept_at timestamptz NOT NULL DEFAULT now(),
  cutoff timestamptz NOT NULL,
  retention_days integer NOT NULL CHECK (retention_days >= 7),
  deleted_runs integer NOT NULL CHECK (deleted_runs >= 0),
  deleted_cells bigint NOT NULL CHECK (deleted_cells >= 0),
  held_runs integer NOT NULL CHECK (held_runs >= 0),
  hold_id uuid,
  actor text NOT NULL CHECK (actor <> ''),
  PRIMARY KEY (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS report_run_sweep_recent
  ON report_run_sweep (tenant_id, swept_at DESC, id DESC);
ALTER TABLE report_run_sweep ENABLE ROW LEVEL SECURITY;
ALTER TABLE report_run_sweep FORCE ROW LEVEL SECURITY;
CREATE POLICY report_run_sweep_tenant_access ON report_run_sweep AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY report_run_sweep_legal_entity_scope ON report_run_sweep AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The audit trail records the run sweeps (the kind list is a CHECK, so it is replaced).
ALTER TABLE audit_trail_entry DROP CONSTRAINT IF EXISTS audit_trail_entry_kind_check;
ALTER TABLE audit_trail_entry ADD CONSTRAINT audit_trail_entry_kind_check CHECK (kind IN (
    'posted', 'replayed', 'reversed', 'batch_submitted', 'batch_approved', 'batch_rejected',
    'period_changed', 'reopen_approved', 'rate_override', 'tax_fallback', 'allocation_run',
    'year_end_run', 'rebuild',
    'statement_run', 'definition_published', 'definition_retired', 'hierarchy_published',
    'schedule_saved', 'audit_exported',
    'legal_hold_placed', 'legal_hold_released', 'library_swept', 'runs_swept'));

-- Cells are removed only with their run.
DO $$
DECLARE
  fk text;
BEGIN
  SELECT conname INTO fk FROM pg_constraint
    WHERE conrelid = 'statement_cell'::regclass AND contype = 'f' AND confrelid = 'statement_run'::regclass;
  IF fk IS NOT NULL THEN
    EXECUTE format('ALTER TABLE statement_cell DROP CONSTRAINT %I', fk);
  END IF;
  -- NOT VALID then VALIDATE: the rows already satisfied the dropped key, and
  -- validating separately scans statement_cell under SHARE UPDATE EXCLUSIVE
  -- instead of the SHARE ROW EXCLUSIVE lock a validating ADD takes.
  ALTER TABLE statement_cell ADD CONSTRAINT statement_cell_run_fk
    FOREIGN KEY (tenant_id, run_id) REFERENCES statement_run (tenant_id, id) ON DELETE CASCADE NOT VALID;
  ALTER TABLE statement_cell VALIDATE CONSTRAINT statement_cell_run_fk;
END
$$;

-- The 7-day floor is 168 hours, the same 24-hour days the sweep's cutoff uses
-- ('7 days' would follow the session time zone across a DST change). The hold
-- locks of a run's entities are taken in uuid order, the order the sweep takes
-- them in before it deletes, so the guard never waits on a lock out of order.
CREATE OR REPLACE FUNCTION statement_run_guard_delete() RETURNS trigger AS $$
DECLARE
  entities uuid[];
  entity uuid;
BEGIN
  IF OLD.started_at > now() - interval '168 hours' THEN
    RAISE EXCEPTION 'statement run % is younger than the lowest ad-hoc retention (7 days)', OLD.id
      USING ERRCODE = 'restrict_violation';
  END IF;
  IF EXISTS (SELECT 1 FROM report_schedule_firing f WHERE f.tenant_id = OLD.tenant_id AND f.run_id = OLD.id)
     OR EXISTS (SELECT 1 FROM report_library_entry e WHERE e.tenant_id = OLD.tenant_id AND e.run_id = OLD.id) THEN
    RAISE EXCEPTION 'statement run % belongs to a scheduled firing', OLD.id
      USING ERRCODE = 'restrict_violation';
  END IF;
  SELECT coalesce(array_agg(DISTINCT e ORDER BY e), '{}') INTO entities
    FROM unnest(coalesce(OLD.legal_entity_ids, '{}') || ARRAY[OLD.legal_entity_id]) AS e WHERE e IS NOT NULL;
  IF cardinality(entities) = 0 THEN
    RAISE EXCEPTION 'statement run % has no legal entity, so no legal hold could cover it', OLD.id
      USING ERRCODE = 'restrict_violation';
  END IF;
  FOREACH entity IN ARRAY entities LOOP
    PERFORM report_legal_hold_lock(OLD.tenant_id, entity);
    IF EXISTS (SELECT 1 FROM report_legal_hold h
               WHERE h.tenant_id = OLD.tenant_id AND h.legal_entity_id = entity AND h.released_at IS NULL) THEN
      RAISE EXCEPTION 'statement run % is under legal hold of legal entity %', OLD.id, entity
        USING ERRCODE = 'restrict_violation';
    END IF;
  END LOOP;
  RETURN OLD;
END
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS statement_run_guard_delete ON statement_run;
CREATE TRIGGER statement_run_guard_delete BEFORE DELETE ON statement_run
  FOR EACH ROW EXECUTE FUNCTION statement_run_guard_delete();

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON report_run_sweep TO passage_app';
    EXECUTE 'GRANT DELETE ON statement_run TO passage_app';
    -- The guard reads started_at and the legal entities of the run, so the
    -- application role must not be able to move them: table-wide UPDATE
    -- (migration 0007) becomes UPDATE of duration_ms, the only column the store
    -- updates after the insert.
    EXECUTE 'REVOKE UPDATE ON statement_run FROM passage_app';
    EXECUTE 'GRANT UPDATE (duration_ms) ON statement_run TO passage_app';
  END IF;
END
$$;
