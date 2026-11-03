-- PSG-25 slice 17: the downtime reason code master (FR-25.11, D4). Until now a downtime was coded
-- with free text (downtime_record.reason_code); a supervisor maintains the list of codes here, each
-- with a name and the planned or unplanned category that OEE and andon report on, and coding a
-- downtime then takes one of the active codes. The code is versioned (an update carries the version
-- it read, If-Match) and never changes after creation. A tenant with no reason defined keeps the
-- free text, so nothing coded before this migration is rewritten. New table, no lock on a live one.

CREATE TABLE IF NOT EXISTS downtime_reason (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (char_length(code) BETWEEN 1 AND 32),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  category text NOT NULL CHECK (category IN ('planned', 'unplanned')),
  active boolean NOT NULL DEFAULT true,
  version integer NOT NULL DEFAULT 1,
  created_by text NOT NULL CHECK (char_length(created_by) BETWEEN 1 AND 200),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
ALTER TABLE downtime_reason ENABLE ROW LEVEL SECURITY;
ALTER TABLE downtime_reason FORCE ROW LEVEL SECURITY;
CREATE POLICY downtime_reason_tenant_access ON downtime_reason AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON downtime_reason TO passage_app';
  END IF;
END
$$;
