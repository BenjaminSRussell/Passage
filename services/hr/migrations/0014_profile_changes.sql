-- #267: employee self-service profile changes. The home address joins the PII
-- vault. A preferred-name change applies at once; a home address or bank
-- account change waits for approval with its new value sealed on the change
-- row, and the sealed value is dropped once the change is decided or withdrawn.
ALTER TABLE worker_pii ADD COLUMN IF NOT EXISTS home_address_sealed bytea;

CREATE TABLE IF NOT EXISTS hr_profile_change (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  worker_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('PREFERRED_NAME', 'HOME_ADDRESS', 'BANK_ACCOUNT')),
  status text NOT NULL CHECK (status IN ('APPLIED', 'PENDING', 'APPROVED', 'REJECTED', 'CANCELED')),
  summary text NOT NULL,
  value_sealed bytea,
  requested_by text NOT NULL,
  decided_by text,
  decided_at timestamptz,
  decision_note text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id),
  CHECK ((status = 'PENDING') = (value_sealed IS NOT NULL)),
  CHECK ((kind = 'PREFERRED_NAME') = (status = 'APPLIED')),
  CHECK ((status IN ('APPROVED', 'REJECTED')) = (decided_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS hr_profile_change_worker ON hr_profile_change (tenant_id, worker_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS hr_profile_change_one_pending ON hr_profile_change (tenant_id, worker_id, kind) WHERE status = 'PENDING';

ALTER TABLE hr_profile_change ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_profile_change FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS hr_profile_change_tenant_access ON hr_profile_change;
CREATE POLICY hr_profile_change_tenant_access ON hr_profile_change AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON hr_profile_change TO passage_app';
    EXECUTE 'GRANT UPDATE (home_address_sealed) ON worker_pii TO passage_app';
  END IF;
END
$$;
