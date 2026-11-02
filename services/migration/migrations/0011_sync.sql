-- Scheduled delta sync (PSG-10 integration track S4). The scheduler runs inside this service; a binding's schedule is
-- the scheduleCron of the migrate project that names its connection profile.
--
-- sync_state: one row per entity binding that has been scheduled or configured (version 0 until an operator edits it). The watermark is written by the same
-- statement that ends a claim, after the run's loads committed, so a failed or abandoned run leaves it unchanged. A
-- replica takes a due binding with one conditional UPDATE on the database clock (claim_id / claimed_at); a claim
-- older than the run deadline plus a margin (40 minutes, as on migration.project) counts as abandoned.
CREATE TABLE migration.sync_state (
  tenant_id uuid NOT NULL,
  binding_id uuid NOT NULL,
  project_id uuid,
  schedule_cron text,
  source_of_truth text NOT NULL DEFAULT 'legacy' CHECK (source_of_truth IN ('legacy', 'passage')),
  paused boolean NOT NULL DEFAULT false,
  watermark text NOT NULL DEFAULT '',
  -- The entity binding version the watermark was read under: after the binding is edited (another table, query or
  -- cursor field) the old watermark means nothing, so the next run reads everything again.
  watermark_binding_version integer,
  next_due_at timestamptz,
  claim_id uuid,
  claimed_at timestamptz,
  last_run_id uuid,
  last_run_at timestamptz,
  last_status text NOT NULL DEFAULT '' CHECK (last_status IN ('', 'succeeded', 'partial', 'failed', 'abandoned')),
  last_error text NOT NULL DEFAULT '',
  last_success_at timestamptz,
  consecutive_failures integer NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
  rows_loaded bigint NOT NULL DEFAULT 0,
  rows_rejected bigint NOT NULL DEFAULT 0,
  version integer NOT NULL DEFAULT 0 CHECK (version >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, binding_id),
  FOREIGN KEY (tenant_id, binding_id) REFERENCES migration.entity_binding (tenant_id, id),
  CHECK ((claim_id IS NULL) = (claimed_at IS NULL))
);
CREATE INDEX sync_state_due_idx ON migration.sync_state (next_due_at) WHERE NOT paused AND schedule_cron IS NOT NULL;

-- sync_conflict: a legacy row held back because Passage is the source of truth. One row per (binding, source key);
-- "kept" remembers the legacy content hash the operator left out so the same row is not reported again.
CREATE TABLE migration.sync_conflict (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  binding_id uuid NOT NULL,
  run_id uuid,
  source_key text NOT NULL,
  target_id text NOT NULL DEFAULT '',
  legacy_hash text NOT NULL,
  legacy_fields jsonb NOT NULL,
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'kept', 'accepted')),
  detected_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz,
  resolved_by text NOT NULL DEFAULT '',
  -- Bumped whenever a run replaces the held legacy values or an operator resolves it; a resolution names the version
  -- it was decided on, so a row that changed in the meantime is not closed unseen.
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, binding_id, source_key),
  FOREIGN KEY (tenant_id, binding_id) REFERENCES migration.entity_binding (tenant_id, id)
);
CREATE INDEX sync_conflict_status_idx ON migration.sync_conflict (tenant_id, status, detected_at DESC, id);

-- Runs started by the scheduler name the binding they synced.
ALTER TABLE migration.run ADD COLUMN binding_id uuid;
CREATE INDEX run_binding_idx ON migration.run (tenant_id, binding_id, created_at DESC, id DESC) WHERE binding_id IS NOT NULL;

-- Outbox for sync-run events (written in the transaction that ends the claim; Debezium relays it).
CREATE TABLE migration.outbox (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL,
  aggregate_type text NOT NULL,
  aggregate_id text NOT NULL,
  type text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['sync_state', 'sync_conflict', 'outbox'] LOOP
    EXECUTE format('ALTER TABLE migration.%I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE migration.%I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON migration.%I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
  END LOOP;
  EXECUTE 'CREATE POLICY sync_state_legal_entity_scope ON migration.sync_state AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'')';
  EXECUTE 'CREATE POLICY sync_conflict_legal_entity_scope ON migration.sync_conflict AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'')';
END
$$;

-- The scheduler has to look across tenants once per tick to learn which tenants have something to do; row-level
-- security hides that from the application role, so the lookup is a SECURITY DEFINER function (the pattern of
-- batch_dispatchable_runs) that returns identifiers only: no schedule, source or row data, and it claims nothing.
-- The scheduler re-reads the binding's schedule, claims it and runs it in the tenant's own transaction under RLS.
-- Each binding is scheduled by the lowest-id active scheduled migrate project on its profile (as the sync-status view
-- shows); candidates come round-robin across tenants, most overdue first, so one tenant's backlog cannot starve
-- another's. The function owner must bypass RLS (the migration role does).
CREATE FUNCTION migration.sync_candidates(max_rows int)
  RETURNS TABLE (tenant_id uuid, binding_id uuid)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp
  AS $$
    SELECT c.tenant_id, c.binding_id FROM (
      SELECT b.tenant_id, b.id AS binding_id, s.next_due_at,
        pg_catalog.row_number() OVER (PARTITION BY b.tenant_id ORDER BY s.next_due_at NULLS FIRST, b.id) AS turn
      FROM migration.entity_binding b
      JOIN migration.connection_profile cp ON cp.tenant_id = b.tenant_id AND cp.id = b.connection_profile_id AND cp.status = 'active'
      CROSS JOIN LATERAL (
        SELECT p.id, p.schedule_cron FROM migration.project p
        WHERE p.tenant_id = b.tenant_id AND p.connection_profile_id = b.connection_profile_id AND p.project_type = 'migrate'
          AND p.status = 'active' AND p.schedule_cron IS NOT NULL AND p.schedule_cron <> ''
        ORDER BY p.id LIMIT 1) p
      LEFT JOIN migration.sync_state s ON s.tenant_id = b.tenant_id AND s.binding_id = b.id
      WHERE b.status = 'active'
        AND (s.binding_id IS NULL
             OR s.schedule_cron IS DISTINCT FROM p.schedule_cron
             OR s.project_id IS DISTINCT FROM p.id
             OR (NOT s.paused AND s.next_due_at <= pg_catalog.now()
                 AND (s.claim_id IS NULL OR s.claimed_at < pg_catalog.now() - pg_catalog.make_interval(secs => 2400))))
    ) c
    ORDER BY c.turn, c.next_due_at NULLS FIRST, c.tenant_id, c.binding_id
    LIMIT LEAST(GREATEST(max_rows, 1), 1000)
  $$;
CREATE FUNCTION migration.sync_can_scan()
  RETURNS boolean
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp
  AS $$ SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND (rolsuper OR rolbypassrls)) $$;
REVOKE ALL ON FUNCTION migration.sync_candidates(int) FROM PUBLIC;
REVOKE ALL ON FUNCTION migration.sync_can_scan() FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    -- UPDATE: the claim, the end of a run and pause/resume change sync_state; a conflict is resolved in place and
    -- re-opened when the legacy row changes again. Nothing is deleted. The outbox is append-only.
    GRANT SELECT, INSERT, UPDATE ON migration.sync_state, migration.sync_conflict TO passage_app;
    GRANT SELECT, INSERT ON migration.outbox TO passage_app;
    GRANT EXECUTE ON FUNCTION migration.sync_candidates(int) TO passage_app;
    GRANT EXECUTE ON FUNCTION migration.sync_can_scan() TO passage_app;
  END IF;
END
$$;
