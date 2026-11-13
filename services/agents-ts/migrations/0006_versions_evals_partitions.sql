-- Agent version registry and eval runs (FR-47.10, BR-47.8), and agent_action partitioned by month (9.47.4).
-- Migrations must run as the owner role, as in 0001.

DO $do$
BEGIN
  IF current_user = 'passage_app' THEN
    RAISE EXCEPTION 'agents migrations must run as the owner role, not passage_app';
  END IF;
END
$do$;

-- agent_version: one pinned definition (prompt, tools, model, duties). The catalog is the platform's, the same for
-- every tenant, so the table has no tenant column (allowlisted in tools/rlscheck). A deployment can pin a different
-- model for the same version number (AGENTS_MODEL, AGENTS_MODEL_OVERRIDES), so a definition is keyed by its prompt
-- hash too (registry.ts promptHash: system prompt, model id and sorted tool list); an eval passes one exact
-- definition, never a version number alone. Rows are written by the pods at start and never change.
CREATE TABLE IF NOT EXISTS agent_version (
  agent_id text NOT NULL CHECK (agent_id ~ '^[a-z][a-z0-9_]{0,63}$'),
  version integer NOT NULL CHECK (version >= 1),
  prompt_hash text NOT NULL CHECK (prompt_hash ~ '^[0-9a-f]{64}$'),
  model_id text NOT NULL CHECK (char_length(model_id) BETWEEN 1 AND 200),
  system_prompt text NOT NULL CHECK (char_length(system_prompt) BETWEEN 1 AND 100000),
  allowed_tools jsonb NOT NULL CHECK (jsonb_typeof(allowed_tools) = 'array'),
  required_duties jsonb NOT NULL CHECK (jsonb_typeof(required_duties) = 'array'),
  registered_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (agent_id, version, prompt_hash)
);

-- eval_run: one gate result of the eval pipeline for one definition on one dataset version (evals/src/run.ts
-- report). Append-only. The latest run of a definition decides whether it may run (AGT-0204), so a later failing
-- run on a new dataset version blocks a definition that passed before (BR-47.8).
CREATE TABLE IF NOT EXISTS eval_run (
  eval_run_id uuid PRIMARY KEY,
  agent_id text NOT NULL,
  version integer NOT NULL,
  prompt_hash text NOT NULL,
  dataset_id text NOT NULL CHECK (char_length(dataset_id) BETWEEN 1 AND 200),
  dataset_version text NOT NULL CHECK (char_length(dataset_version) BETWEEN 1 AND 200),
  passed boolean NOT NULL,
  metrics jsonb NOT NULL CHECK (jsonb_typeof(metrics) = 'object'),
  failures jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(failures) = 'array'),
  recorded_by text NOT NULL CHECK (char_length(recorded_by) BETWEEN 1 AND 200),
  recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  FOREIGN KEY (agent_id, version, prompt_hash) REFERENCES agent_version (agent_id, version, prompt_hash),
  CHECK (passed = (jsonb_array_length(failures) = 0))
);
CREATE INDEX IF NOT EXISTS eval_run_latest ON eval_run (agent_id, version, prompt_hash, recorded_at DESC);

-- agent_action partitioned by the month its run started (UTC). Every action of a run lands in the run's partition,
-- so (tenant_id, run_id, seq) stays unique: the primary key carries run_month, and the foreign key to
-- agent_run (tenant_id, run_id, run_month) pins run_month to the run's own month. A month of runs and their
-- actions can be detached and archived together.
ALTER TABLE agent_run ADD COLUMN IF NOT EXISTS run_month date
  GENERATED ALWAYS AS ((date_trunc('month', created_at AT TIME ZONE 'UTC'))::date) STORED;
ALTER TABLE agent_run ADD CONSTRAINT agent_run_month_key UNIQUE (tenant_id, run_id, run_month);

-- created_at now decides the partition of the run's actions, so it joins the identity a run cannot change.
CREATE OR REPLACE FUNCTION agent_run_guard() RETURNS trigger LANGUAGE plpgsql AS $guard$
BEGIN
  IF OLD.status <> 'RUNNING' THEN
    RAISE EXCEPTION 'agent_run % is finished and cannot change', OLD.run_id;
  END IF;
  IF NEW.tenant_id <> OLD.tenant_id OR NEW.run_id <> OLD.run_id OR NEW.agent_id <> OLD.agent_id
     OR NEW.agent_version <> OLD.agent_version OR NEW.invoker_user_id <> OLD.invoker_user_id
     OR NEW.created_at <> OLD.created_at THEN
    RAISE EXCEPTION 'agent_run % identity cannot change', OLD.run_id;
  END IF;
  RETURN NEW;
END
$guard$;

CREATE TABLE agent_action_p (
  tenant_id uuid NOT NULL,
  run_id text NOT NULL,
  run_month date NOT NULL,
  seq integer NOT NULL CHECK (seq >= 1),
  tool_name text NOT NULL,
  input_json text NOT NULL,
  output_hash text NOT NULL CHECK (output_hash ~ '^[0-9a-f]{64}$'),
  output_json text NOT NULL,
  output_truncated boolean NOT NULL,
  approver_user_id text,
  decision text NOT NULL CHECK (decision IN ('APPROVED', 'REJECTED', 'AUTO', 'DENIED')),
  decided_at timestamptz NOT NULL,
  committed_entity_ref text,
  reason text,
  CONSTRAINT agent_action_month_pkey PRIMARY KEY (tenant_id, run_id, seq, run_month),
  CONSTRAINT agent_action_run_fkey FOREIGN KEY (tenant_id, run_id, run_month) REFERENCES agent_run (tenant_id, run_id, run_month),
  CHECK (committed_entity_ref IS NULL OR decision IN ('APPROVED', 'AUTO'))
) PARTITION BY RANGE (run_month);
-- Rows of a month without its partition land here; agent_action_create_partitions moves them out.
CREATE TABLE agent_action_p_default PARTITION OF agent_action_p DEFAULT;

-- Creates the monthly partitions from `first_month` to `last_month` (inclusive) that do not exist yet. Rows of
-- those months already in the default partition move into the new partition before it is attached. Owner only.
-- Every name is qualified with the function's own schema (its search_path below is that schema, then pg_temp):
-- an unqualified lookup would try the caller's temporary tables first.
CREATE OR REPLACE FUNCTION agent_action_create_partitions(first_month date, last_month date) RETURNS integer
LANGUAGE plpgsql AS $fn$
DECLARE
  s text := current_schema();
  m date := date_trunc('month', first_month)::date;
  stop date := date_trunc('month', last_month)::date;
  next_m date;
  part text;
  made integer := 0;
BEGIN
  IF stop < m OR stop > m + interval '20 years' THEN
    RAISE EXCEPTION 'agent_action partitions: % to % is not a range of months', first_month, last_month;
  END IF;
  -- One creator at a time, so two pods never race to create or attach the same month.
  PERFORM pg_advisory_xact_lock(47470006);
  WHILE m <= stop LOOP
    next_m := (m + interval '1 month')::date;
    part := 'agent_action_' || to_char(m, 'YYYYMM');
    IF to_regclass(format('%I.%I', s, part)) IS NULL THEN
      EXECUTE format('CREATE TABLE %I.%I (LIKE %I.agent_action INCLUDING DEFAULTS INCLUDING CONSTRAINTS)', s, part, s);
      EXECUTE format(
        'WITH moved AS (DELETE FROM %I.agent_action_default WHERE run_month >= %L AND run_month < %L RETURNING *) INSERT INTO %I.%I SELECT * FROM moved',
        s, m, next_m, s, part);
      EXECUTE format('ALTER TABLE %I.agent_action ATTACH PARTITION %I.%I FOR VALUES FROM (%L) TO (%L)', s, s, part, m, next_m);
      made := made + 1;
    END IF;
    m := next_m;
  END LOOP;
  RETURN made;
END
$fn$;

-- What the pods call at start and daily: this month and the next `months_ahead` (0 to 12). SECURITY DEFINER so the
-- application role can create partitions without owning the table; it can do nothing else through it.
CREATE OR REPLACE FUNCTION agent_action_ensure_partitions(months_ahead integer) RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER AS $fn$
DECLARE
  this_month date := date_trunc('month', now() AT TIME ZONE 'UTC')::date;
BEGIN
  IF months_ahead IS NULL OR months_ahead < 0 OR months_ahead > 12 THEN
    RAISE EXCEPTION 'months_ahead must be 0 to 12';
  END IF;
  RETURN agent_action_create_partitions(this_month, (this_month + make_interval(months => months_ahead))::date);
END
$fn$;
-- Pinned to the schema the migration runs in, with pg_temp searched last (CREATE FUNCTION ... SECURITY DEFINER,
-- "Writing SECURITY DEFINER Functions Safely").
DO $sp$
BEGIN
  EXECUTE format('ALTER FUNCTION agent_action_create_partitions(date, date) SET search_path = %I, pg_temp', current_schema());
  EXECUTE format('ALTER FUNCTION agent_action_ensure_partitions(integer) SET search_path = %I, pg_temp', current_schema());
END
$sp$;
REVOKE ALL ON FUNCTION agent_action_create_partitions(date, date) FROM PUBLIC;
REVOKE ALL ON FUNCTION agent_action_ensure_partitions(integer) FROM PUBLIC;

-- Move the existing rows. The owner is bound by FORCE row-level security like everyone else, so it is lifted for
-- the copy and restored before the transaction ends.
ALTER TABLE agent_run NO FORCE ROW LEVEL SECURITY;
ALTER TABLE agent_action NO FORCE ROW LEVEL SECURITY;
ALTER TABLE agent_action RENAME TO agent_action_unpartitioned;
ALTER TABLE agent_action_p RENAME TO agent_action;
ALTER TABLE agent_action_p_default RENAME TO agent_action_default;
DO $copy$
DECLARE
  oldest date;
  this_month date := date_trunc('month', now() AT TIME ZONE 'UTC')::date;
BEGIN
  SELECT min(r.run_month) INTO oldest FROM agent_action_unpartitioned a JOIN agent_run r USING (tenant_id, run_id);
  PERFORM agent_action_create_partitions(least(coalesce(oldest, this_month), this_month), (this_month + interval '2 months')::date);
END
$copy$;
INSERT INTO agent_action (tenant_id, run_id, run_month, seq, tool_name, input_json, output_hash, output_json, output_truncated,
                          approver_user_id, decision, decided_at, committed_entity_ref, reason)
SELECT a.tenant_id, a.run_id, r.run_month, a.seq, a.tool_name, a.input_json, a.output_hash, a.output_json, a.output_truncated,
       a.approver_user_id, a.decision, a.decided_at, a.committed_entity_ref, a.reason
FROM agent_action_unpartitioned a JOIN agent_run r USING (tenant_id, run_id);
DROP TABLE agent_action_unpartitioned;
ALTER TABLE agent_run FORCE ROW LEVEL SECURITY;

ALTER TABLE agent_action ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_action FORCE ROW LEVEL SECURITY;
CREATE POLICY agent_action_tenant_access ON agent_action AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- The application role reaches agent_action only through the parent (partitions carry no grant). Versions and eval
-- runs are append-only for it.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON agent_action TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON agent_version TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON eval_run TO passage_app';
    EXECUTE 'GRANT EXECUTE ON FUNCTION agent_action_ensure_partitions(integer) TO passage_app';
  END IF;
END
$$;
