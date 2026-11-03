-- Per-binding sync schedules (PSG-10 integration track S4). A binding used to take the scheduleCron of the lowest-id
-- active migrate project on its profile and nothing else, so a customer's customers table and its rarely-changing
-- price table ran on the same clock. schedule_override is the binding's own cron (five fields, UTC, at least 15
-- minutes between runs, checked by the API); when it is set it wins, when it is null the project's cron stays the
-- default. schedule_cron keeps holding the effective cron the next_due_at was computed from, so the scheduler sees a
-- change of either source as "schedule changed" and moves next_due_at to the next slot of the new cron.
-- The override lives on sync_state, not on entity_binding: editing a binding bumps its version and discards the
-- watermark (the old cursor means nothing), which a schedule change must not do.
-- A binding with an override still runs under an active migrate project of its profile (one active run per project,
-- BR-10.2); that project need not have a schedule itself.
-- Locking: sync_state holds one row per scheduled binding and the scheduler updates it every pass. A nullable column
-- without a default rewrites nothing; the CHECK is validated by one scan of that small table (the column is null in
-- every row) under the ACCESS EXCLUSIVE lock. The lock_timeout makes the migration fail and the rollout retry instead
-- of queueing behind a long transaction while every claim queues behind it.
SET LOCAL lock_timeout = '5s';

ALTER TABLE migration.sync_state
  ADD COLUMN schedule_override text CHECK (schedule_override IS NULL OR (schedule_override <> '' AND char_length(schedule_override) <= 100));

-- Same contract as in 0011: identifiers only, claims nothing. The binding's project is the lowest-id active migrate
-- project on its profile that has a schedule (a project without one only when the binding has its own), and the
-- schedule is the binding's override or that project's cron.
CREATE OR REPLACE FUNCTION migration.sync_candidates(max_rows int)
  RETURNS TABLE (tenant_id uuid, binding_id uuid)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp
  AS $$
    SELECT c.tenant_id, c.binding_id FROM (
      SELECT b.tenant_id, b.id AS binding_id, s.next_due_at,
        pg_catalog.row_number() OVER (PARTITION BY b.tenant_id ORDER BY s.next_due_at NULLS FIRST, b.id) AS turn
      FROM migration.entity_binding b
      JOIN migration.connection_profile cp ON cp.tenant_id = b.tenant_id AND cp.id = b.connection_profile_id AND cp.status = 'active'
      LEFT JOIN migration.sync_state s ON s.tenant_id = b.tenant_id AND s.binding_id = b.id
      CROSS JOIN LATERAL (
        SELECT p.id, coalesce(nullif(s.schedule_override, ''), p.schedule_cron) AS cron FROM migration.project p
        WHERE p.tenant_id = b.tenant_id AND p.connection_profile_id = b.connection_profile_id AND p.project_type = 'migrate'
          AND p.status = 'active'
          AND (coalesce(s.schedule_override, '') <> '' OR (p.schedule_cron IS NOT NULL AND p.schedule_cron <> ''))
        ORDER BY (coalesce(p.schedule_cron, '') = ''), p.id LIMIT 1) p
      WHERE b.status = 'active'
        AND (s.binding_id IS NULL
             OR s.schedule_cron IS DISTINCT FROM p.cron
             OR s.project_id IS DISTINCT FROM p.id
             OR (NOT s.paused AND s.next_due_at <= pg_catalog.now()
                 AND (s.claim_id IS NULL OR s.claimed_at < pg_catalog.now() - pg_catalog.make_interval(secs => 2400))))
    ) c
    ORDER BY c.turn, c.next_due_at NULLS FIRST, c.tenant_id, c.binding_id
    LIMIT LEAST(GREATEST(max_rows, 1), 1000)
  $$;
