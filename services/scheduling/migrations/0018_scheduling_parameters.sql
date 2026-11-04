-- Wave 23 slice 24: scheduling parameters (PRD 4.23.13 site parameters).
--
-- One row per tenant holds the defaults a planner would otherwise retype on
-- every run: mode, direction and finite or infinite for runs that leave them
-- out, the drag snap of the Gantt, and the BR-23.10 propagation limits a
-- propagated move walks. No row means the built-in defaults (version 1); the
-- first change inserts the row, so the version is the If-Match of every change.

CREATE TABLE IF NOT EXISTS scheduling_parameters (
  tenant_id uuid NOT NULL,
  default_mode text NOT NULL DEFAULT 'jobs' CHECK (default_mode IN ('jobs', 'operations')),
  default_direction text NOT NULL DEFAULT 'forward' CHECK (default_direction IN ('forward', 'backward')),
  default_finite boolean NOT NULL DEFAULT true,
  drag_snap_minutes integer NOT NULL DEFAULT 5 CHECK (drag_snap_minutes BETWEEN 1 AND 60),
  propagation_orders integer NOT NULL DEFAULT 500 CHECK (propagation_orders BETWEEN 1 AND 5000),
  propagation_levels integer NOT NULL DEFAULT 5 CHECK (propagation_levels BETWEEN 1 AND 20),
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id)
);
ALTER TABLE scheduling_parameters ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduling_parameters FORCE ROW LEVEL SECURITY;
CREATE POLICY scheduling_parameters_tenant_access ON scheduling_parameters AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON scheduling_parameters TO passage_app';
  END IF;
END
$$;
