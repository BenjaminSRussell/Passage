-- Customer groups (#500). customer_role.customer_group_id names a group that had
-- no record of its own, so a form could only ask for the group's id. A customer
-- group now has a short code and a name, like a vendor group (0011): code
-- uppercase A-Z, 0-9, dash or underscore, at most 10 characters, unique per
-- tenant. customer_role keeps its plain customer_group_id: roles that name a
-- group not registered yet stay valid.
CREATE TABLE IF NOT EXISTS customer_group (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9_-]{1,10}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
CREATE UNIQUE INDEX IF NOT EXISTS customer_group_idempotency_key ON customer_group (tenant_id, idempotency_key);

ALTER TABLE customer_group ENABLE ROW LEVEL SECURITY;
ALTER TABLE customer_group FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS customer_group_tenant_access ON customer_group;
CREATE POLICY customer_group_tenant_access ON customer_group AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Backfill: every customer_group_id already on a customer role becomes a group
-- with the code 'CG' + the first 8 hex digits of its id, falling back to
-- 'C' + 9 hex, 10 hex, then 'G' + hex digits 9 to 17 on a collision, in tenant
-- and id order (the same rule as the vendor group backfill). It reads every
-- tenant, so it runs only under a role that bypasses row-level security; under
-- passage_app it is skipped with a notice and the groups are registered through
-- POST /v1/party/customer-groups with their existing id.
DO $$
DECLARE
  r record;
  h text;
  candidate text;
  chosen text;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND (rolsuper OR rolbypassrls)) THEN
    RAISE NOTICE 'customer group backfill skipped: role % does not bypass row-level security', current_user;
    RETURN;
  END IF;
  FOR r IN SELECT DISTINCT tenant_id, customer_group_id AS id FROM customer_role ORDER BY tenant_id, customer_group_id LOOP
    CONTINUE WHEN EXISTS (SELECT 1 FROM customer_group g WHERE g.tenant_id = r.tenant_id AND g.id = r.id);
    h := upper(replace(r.id::text, '-', ''));
    chosen := NULL;
    FOREACH candidate IN ARRAY ARRAY['CG' || left(h, 8), 'C' || left(h, 9), left(h, 10), 'G' || substr(h, 9, 9)] LOOP
      IF NOT EXISTS (SELECT 1 FROM customer_group g WHERE g.tenant_id = r.tenant_id AND g.code = candidate) THEN
        chosen := candidate;
        EXIT;
      END IF;
    END LOOP;
    IF chosen IS NULL THEN
      RAISE EXCEPTION 'no free customer group code for %', r.id;
    END IF;
    INSERT INTO customer_group (tenant_id, id, code, name) VALUES (r.tenant_id, r.id, chosen, 'Customer group ' || chosen);
  END LOOP;
END
$$;

-- Groups are created and read; the code is never updated or deleted.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON customer_group TO passage_app';
  END IF;
END
$$;
