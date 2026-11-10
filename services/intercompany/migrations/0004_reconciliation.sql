-- Wave 40 (#40-4): stored reconciliation runs (FR-40.6). A run classifies
-- every entity pair of a period as Matched, Timing or Mismatch and is never
-- changed afterwards; an item that needs action starts open and is resolved
-- with a note, the only change an item allows. Every table carries tenant_id
-- and is tenant-scoped by row-level security.

CREATE TABLE IF NOT EXISTS ic_reconciliation_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  period text NOT NULL CHECK (period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  fx_tolerance_pct numeric(6,3) NOT NULL CHECK (fx_tolerance_pct >= 0 AND fx_tolerance_pct <= 5),
  request_hash text NOT NULL,
  matched integer NOT NULL CHECK (matched >= 0),
  timing integer NOT NULL CHECK (timing >= 0),
  mismatch integer NOT NULL CHECK (mismatch >= 0),
  created_by text NOT NULL CHECK (length(created_by) >= 1),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS ic_reconciliation_run_recent ON ic_reconciliation_run (tenant_id, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS ic_reconciliation_item (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  run_id uuid NOT NULL,
  seq integer NOT NULL,
  entity text NOT NULL CHECK (length(entity) BETWEEN 1 AND 64),
  counterparty text NOT NULL CHECK (length(counterparty) BETWEEN 1 AND 64),
  ref text NOT NULL DEFAULT '',
  amount_a numeric(28,6) NOT NULL,
  amount_b numeric(28,6) NOT NULL,
  next_period_a numeric(28,6) NOT NULL DEFAULT 0,
  next_period_b numeric(28,6) NOT NULL DEFAULT 0,
  fx_applies boolean NOT NULL DEFAULT false,
  status text NOT NULL CHECK (status IN ('Matched', 'Timing', 'Mismatch')),
  difference numeric(28,6) NOT NULL,
  tolerance_applied boolean NOT NULL DEFAULT false,
  resolution text NOT NULL CHECK (resolution IN ('none', 'open', 'resolved')),
  note text NOT NULL DEFAULT '',
  resolved_by text,
  resolved_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, run_id, seq),
  FOREIGN KEY (tenant_id, run_id) REFERENCES ic_reconciliation_run (tenant_id, id),
  CHECK ((status = 'Matched') = (resolution = 'none')),
  CHECK ((resolution = 'resolved') = (resolved_at IS NOT NULL AND length(note) >= 1))
);
CREATE INDEX IF NOT EXISTS ic_reconciliation_item_open ON ic_reconciliation_item (tenant_id, run_id, resolution);

CREATE OR REPLACE FUNCTION ic_reconciliation_reject_change() RETURNS trigger
  LANGUAGE plpgsql AS $$
BEGIN
  IF TG_TABLE_NAME = 'ic_reconciliation_item' AND TG_OP = 'UPDATE' THEN
    IF OLD.resolution = 'open'
       AND NEW.resolution = 'resolved'
       AND (NEW.tenant_id, NEW.id, NEW.run_id, NEW.seq, NEW.entity, NEW.counterparty, NEW.ref, NEW.amount_a, NEW.amount_b,
            NEW.next_period_a, NEW.next_period_b, NEW.fx_applies, NEW.status, NEW.difference, NEW.tolerance_applied)
           IS NOT DISTINCT FROM
           (OLD.tenant_id, OLD.id, OLD.run_id, OLD.seq, OLD.entity, OLD.counterparty, OLD.ref, OLD.amount_a, OLD.amount_b,
            OLD.next_period_a, OLD.next_period_b, OLD.fx_applies, OLD.status, OLD.difference, OLD.tolerance_applied) THEN
      RETURN NEW;
    END IF;
  END IF;
  RAISE EXCEPTION 'reconciliation data is immutable: an open item can only be resolved'
    USING ERRCODE = '55000';
END
$$;

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['ic_reconciliation_run', 'ic_reconciliation_item'] LOOP
    EXECUTE format('CREATE TRIGGER %I_immutable BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION ic_reconciliation_reject_change()', t, t);
    EXECUTE format('CREATE TRIGGER %I_no_truncate BEFORE TRUNCATE ON %I FOR EACH STATEMENT EXECUTE FUNCTION ic_reconciliation_reject_change()', t, t);
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format($p$CREATE POLICY %I_tenant_access ON %I AS PERMISSIVE
      USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
      WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid)$p$, t, t);
  END LOOP;
END
$$;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON ic_reconciliation_run TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON ic_reconciliation_item TO passage_app';
  END IF;
END
$$;
