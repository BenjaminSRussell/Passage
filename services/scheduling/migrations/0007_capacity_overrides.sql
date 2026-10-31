-- Wave 23 slice 9: capacity overrides (FR-23.1, AC-23.7).
--
-- An override changes a resource's open capacity for one UTC span without
-- touching its weekly calendar: a downtime (breakdown, maintenance, holiday
-- cover) closes the span, an extra shift opens it. Runs, moves, the capacity
-- inquiry, block utilization and weekly load read the calendar with the
-- overrides applied; a downtime wins over an extra shift that overlaps it.
-- Creating a downtime reports the booked jobs it overlaps so the planner can
-- move them (SCHED-0002 on the next move or run).

CREATE TABLE IF NOT EXISTS scheduling_capacity_override (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  resource_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('downtime', 'extra')),
  start_at timestamptz NOT NULL,
  end_at timestamptz NOT NULL,
  reason text NOT NULL DEFAULT '' CHECK (char_length(reason) <= 200),
  idempotency_key text,
  idempotency_fingerprint text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (end_at > start_at AND end_at - start_at <= interval '31 days'),
  FOREIGN KEY (tenant_id, resource_id) REFERENCES scheduling_resource (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS scheduling_capacity_override_resource ON scheduling_capacity_override (tenant_id, resource_id, start_at);
ALTER TABLE scheduling_capacity_override ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduling_capacity_override FORCE ROW LEVEL SECURITY;
CREATE POLICY scheduling_capacity_override_tenant_access ON scheduling_capacity_override AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON scheduling_capacity_override TO passage_app';
  END IF;
END
$$;
