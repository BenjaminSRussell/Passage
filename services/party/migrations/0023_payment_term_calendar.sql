CREATE TABLE IF NOT EXISTS payment_calendar (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9_-]{1,20}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
  holidays date[] NOT NULL DEFAULT '{}',
  weekend_days smallint[] NOT NULL DEFAULT '{0,6}' CHECK (weekend_days <@ ARRAY[0,1,2,3,4,5,6]::smallint[] AND cardinality(weekend_days) < 7),
  shift text NOT NULL DEFAULT 'following' CHECK (shift IN ('following', 'preceding', 'modified_following')),
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);

ALTER TABLE payment_calendar ENABLE ROW LEVEL SECURITY;
ALTER TABLE payment_calendar FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS payment_calendar_tenant_access ON payment_calendar;
CREATE POLICY payment_calendar_tenant_access ON payment_calendar AS PERMISSIVE
  USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);

ALTER TABLE payment_term ADD COLUMN IF NOT EXISTS calendar_id uuid;
ALTER TABLE payment_term DROP CONSTRAINT IF EXISTS payment_term_calendar_fk;
ALTER TABLE payment_term ADD CONSTRAINT payment_term_calendar_fk
  FOREIGN KEY (tenant_id, calendar_id) REFERENCES payment_calendar (tenant_id, id);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON payment_calendar TO depot_app';
  END IF;
END
$$;
