-- Wave 23 slice 4: order pegging for move propagation (FR-23.8, BR-23.10)
-- and recurring capacity blocks (FR-23.17).
--
-- A peg says the dependent order consumes the parent order's output, so the
-- dependent cannot start before the parent ends. A move with propagate set
-- pushes the pegged dependents forward behind their parents. Pegs are entered
-- through the API until wave 22 pegging publishes them.

CREATE TABLE IF NOT EXISTS scheduling_order_peg (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  parent_order_id uuid NOT NULL,
  dependent_order_id uuid NOT NULL,
  idempotency_key text,
  idempotency_fingerprint text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, parent_order_id, dependent_order_id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (parent_order_id <> dependent_order_id)
);
CREATE INDEX IF NOT EXISTS scheduling_order_peg_dependent ON scheduling_order_peg (tenant_id, dependent_order_id);
ALTER TABLE scheduling_order_peg ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduling_order_peg FORCE ROW LEVEL SECURITY;
CREATE POLICY scheduling_order_peg_tenant_access ON scheduling_order_peg AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- A capacity block is weekly windows, one dated window, or a recurrence: an
-- RRULE subset (FREQ=DAILY|WEEKLY, INTERVAL, BYDAY, COUNT, UNTIL) from a start
-- date with a site-local window, expanded over the horizon in the resource's
-- time zone when a run or move reads it.
ALTER TABLE scheduling_capacity_block
  ADD COLUMN recurrence_rule text CHECK (recurrence_rule IS NULL OR char_length(recurrence_rule) BETWEEN 1 AND 200),
  ADD COLUMN recurrence_start date,
  ADD COLUMN recurrence_start_minute integer,
  ADD COLUMN recurrence_end_minute integer;
ALTER TABLE scheduling_capacity_block DROP CONSTRAINT IF EXISTS scheduling_capacity_block_check;
ALTER TABLE scheduling_capacity_block ADD CONSTRAINT scheduling_capacity_block_shape CHECK (
  (windows IS NOT NULL AND start_at IS NULL AND end_at IS NULL AND recurrence_rule IS NULL)
  OR (windows IS NULL AND start_at IS NOT NULL AND end_at > start_at AND recurrence_rule IS NULL)
  OR (windows IS NULL AND start_at IS NULL AND end_at IS NULL AND recurrence_rule IS NOT NULL AND recurrence_start IS NOT NULL
      AND recurrence_start_minute >= 0 AND recurrence_end_minute <= 1440 AND recurrence_end_minute > recurrence_start_minute)
);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON scheduling_order_peg TO passage_app';
  END IF;
END
$$;
