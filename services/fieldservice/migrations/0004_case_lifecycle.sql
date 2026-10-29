-- Field service, slice 2 (PSG-4502): case status lifecycle with SLA pause and
-- resume, response and resolution warning instants, emitted SLA alerts, case
-- versions for If-Match, and work-order completion, follow-ups and first-time fix.
-- No new tables: every column lands on a table that already has tenant RLS and
-- the legal-entity scope policy (0001).

ALTER TABLE service_case
  ADD COLUMN IF NOT EXISTS version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  ADD COLUMN IF NOT EXISTS responded_at timestamptz,
  ADD COLUMN IF NOT EXISTS resolved_at timestamptz,
  ADD COLUMN IF NOT EXISTS sla_paused_at timestamptz,
  ADD COLUMN IF NOT EXISTS sla_response_warn_at timestamptz,
  ADD COLUMN IF NOT EXISTS sla_resolution_warn_at timestamptz,
  ADD COLUMN IF NOT EXISTS sla_alerts text[] NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();

-- Cases opened before this migration have no warning instants; reads fall back
-- to the due instant (a late warning rather than an early one). No backfill: the
-- migration runs outside any tenant context, where FORCE RLS hides every row.

-- The SLA monitor reads RUNNING clocks oldest due first (PRD 8.45.19 "partial index on RUNNING").
CREATE INDEX IF NOT EXISTS service_case_sla_running ON service_case (tenant_id, sla_resolution_due)
  WHERE sla_state = 'RUNNING';
CREATE INDEX IF NOT EXISTS service_case_opened ON service_case (tenant_id, opened_at);

-- first_time_fix is set at completion (PRD 8.45.4) and cleared by a follow-up (BR-45.5).
ALTER TABLE service_work_order
  ADD COLUMN IF NOT EXISTS completed_at timestamptz,
  ADD COLUMN IF NOT EXISTS first_time_fix boolean,
  ADD COLUMN IF NOT EXISTS follow_up_of uuid;

ALTER TABLE service_work_order DROP CONSTRAINT IF EXISTS service_work_order_follow_up_fk;
ALTER TABLE service_work_order
  ADD CONSTRAINT service_work_order_follow_up_fk
  FOREIGN KEY (tenant_id, follow_up_of) REFERENCES service_work_order (tenant_id, id);

CREATE INDEX IF NOT EXISTS service_work_order_follow_up ON service_work_order (tenant_id, follow_up_of);
CREATE INDEX IF NOT EXISTS service_work_order_completed ON service_work_order (tenant_id, completed_at);

-- Case transitions, the SLA monitor and the dispatch response stamp update
-- service_case, which 0002 granted append-only. Skipped when the role does not
-- exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT UPDATE ON service_case TO passage_app';
  END IF;
END
$$;
