-- Tenant licences (PSG-50, FR-50.12, BR-50.8). Platform state, not tenant data: the licensed tenant is the key
-- (licensee_id, not a tenant_id column: this table is read by the operator, never inside a tenant) and there is no RLS
-- (allowlisted in tools/rlscheck). The signed payload and its Ed25519 signature are kept as installed.
CREATE TABLE ops_tenant_license (
  licensee_id       uuid PRIMARY KEY,
  issued_at         date NOT NULL,
  expires_at        date NOT NULL,
  full_users        integer NOT NULL CHECK (full_users >= 0),
  operational_users integer NOT NULL CHECK (operational_users >= 0),
  selfservice_users integer NOT NULL CHECK (selfservice_users >= 0),
  grace_days        integer NOT NULL DEFAULT 30 CHECK (grace_days BETWEEN 0 AND 60),
  version           bigint NOT NULL DEFAULT 1,
  payload           bytea NOT NULL,
  signature         bytea NOT NULL,
  installed_by      text NOT NULL,
  installed_at      timestamptz NOT NULL DEFAULT now(),
  CHECK (expires_at >= issued_at)
);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON ops_tenant_license TO passage_app';
  END IF;
END
$$;
