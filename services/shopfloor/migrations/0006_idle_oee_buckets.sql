-- PSG-25 slice 6: the per-machine idle threshold (BR-25.6, parameter "Idle threshold s",
-- default 300, 30 to 3,600) and the stored OEE buckets written by the rollup (FR-25.10,
-- PRD OeeBucket, retention 3 years). Every table is tenant scoped with forced RLS.

ALTER TABLE machine ADD COLUMN IF NOT EXISTS idle_threshold_s integer NOT NULL DEFAULT 300
  CONSTRAINT machine_idle_threshold_range CHECK (idle_threshold_s BETWEEN 30 AND 3600);

-- One row per resource, bucket width and bucket start. The width is in the key because the
-- tenant parameter "OEE bucket minutes" allows 5, 15 or 60. Minutes are rounded to 4 places,
-- ratios are numeric(7,4) per BR-25.5; down_minutes is unplanned downtime (planned - run).
CREATE TABLE IF NOT EXISTS oee_bucket (
  tenant_id uuid NOT NULL,
  resource_id uuid NOT NULL,
  bucket_minutes integer NOT NULL CHECK (bucket_minutes IN (5, 15, 60)),
  bucket_start timestamptz NOT NULL,
  planned_minutes numeric(12,4) NOT NULL CHECK (planned_minutes > 0),
  run_minutes numeric(12,4) NOT NULL CHECK (run_minutes >= 0),
  down_minutes numeric(12,4) NOT NULL CHECK (down_minutes >= 0),
  ideal_cycle_s numeric(10,3) NOT NULL CHECK (ideal_cycle_s > 0),
  total_count bigint NOT NULL CHECK (total_count >= 0),
  good_count bigint NOT NULL CHECK (good_count >= 0),
  availability numeric(7,4) NOT NULL,
  performance numeric(7,4) NOT NULL,
  quality numeric(7,4) NOT NULL,
  oee numeric(7,4) NOT NULL,
  over_rate boolean NOT NULL DEFAULT false,
  source_events integer NOT NULL CHECK (source_events >= 0),
  computed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, resource_id, bucket_minutes, bucket_start),
  CHECK (good_count <= total_count),
  -- PRD invariant: run_minutes + down_minutes <= planned_minutes.
  CHECK (run_minutes + down_minutes <= planned_minutes)
);
ALTER TABLE oee_bucket ENABLE ROW LEVEL SECURITY;
ALTER TABLE oee_bucket FORCE ROW LEVEL SECURITY;
CREATE POLICY oee_bucket_tenant_access ON oee_bucket AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- 0004 created process_message and process_message_delivery without granting them to the
-- application role, so the service could not read or write them under passage_app; the
-- grant is added here because applied migrations are not edited.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON oee_bucket, process_message, process_message_delivery TO passage_app';
  END IF;
END
$$;
