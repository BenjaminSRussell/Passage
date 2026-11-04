-- Time zone of sync schedules (PSG-10 integration track S4). A project's scheduleCron, and the cron a binding sets for
-- itself, used to be read in UTC, so "0 2 * * *" meant 02:00 UTC whatever the customer's plant clock said. The time
-- zone is the project's (an IANA name, "UTC" by default so every existing schedule keeps its meaning): the cron and
-- the zone it is read in are edited together and a binding's own cron runs under the project it runs under.
-- sync_state.schedule_tz holds the zone next_due_at was computed in, as schedule_cron holds the cron, so the scheduler
-- sees a change of the zone as "schedule changed" and moves next_due_at to the next slot in the new zone. The API
-- checks the name against the time zone database; the CHECK only bounds its size.
-- Locking: both columns have a constant default, which rewrites nothing. The lock_timeout makes the migration fail and
-- the rollout retry instead of queueing behind a long transaction while every claim queues behind it.
SET LOCAL lock_timeout = '5s';

ALTER TABLE migration.project
  ADD COLUMN schedule_timezone text NOT NULL DEFAULT 'UTC' CHECK (char_length(schedule_timezone) BETWEEN 1 AND 64);

ALTER TABLE migration.sync_state
  ADD COLUMN schedule_tz text NOT NULL DEFAULT 'UTC' CHECK (char_length(schedule_tz) BETWEEN 1 AND 64);

-- Same contract as in 0018 (identifiers only, claims nothing); a binding whose stored zone differs from its project's
-- is a candidate to be re-seeded.
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
        SELECT p.id, p.schedule_timezone AS tz, coalesce(nullif(s.schedule_override, ''), p.schedule_cron) AS cron FROM migration.project p
        WHERE p.tenant_id = b.tenant_id AND p.connection_profile_id = b.connection_profile_id AND p.project_type = 'migrate'
          AND p.status = 'active'
          AND (coalesce(s.schedule_override, '') <> '' OR (p.schedule_cron IS NOT NULL AND p.schedule_cron <> ''))
        ORDER BY (coalesce(p.schedule_cron, '') = ''), p.id LIMIT 1) p
      WHERE b.status = 'active'
        AND (s.binding_id IS NULL
             OR s.schedule_cron IS DISTINCT FROM p.cron
             OR s.schedule_tz IS DISTINCT FROM p.tz
             OR s.project_id IS DISTINCT FROM p.id
             OR (NOT s.paused AND s.next_due_at <= pg_catalog.now()
                 AND (s.claim_id IS NULL OR s.claimed_at < pg_catalog.now() - pg_catalog.make_interval(secs => 2400))))
    ) c
    ORDER BY c.turn, c.next_due_at NULLS FIRST, c.tenant_id, c.binding_id
    LIMIT LEAST(GREATEST(max_rows, 1), 1000)
  $$;
