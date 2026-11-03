-- Vendor groups (issue #421). A vendor group carries a short code (uppercase
-- A-Z, 0-9, dash, underscore, at most 10 characters, unique per tenant) so it
-- fits a ledger dimension value (max 20 characters; a UUID is 36). vendor_role
-- keeps its plain vendor_group_id: roles loaded before a group is registered
-- stay valid and simply have no code.
CREATE TABLE IF NOT EXISTS vendor_group (
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
CREATE UNIQUE INDEX IF NOT EXISTS vendor_group_idempotency_key ON vendor_group (tenant_id, idempotency_key);

ALTER TABLE vendor_group ENABLE ROW LEVEL SECURITY;
ALTER TABLE vendor_group FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS vendor_group_tenant_access ON vendor_group;
CREATE POLICY vendor_group_tenant_access ON vendor_group AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Backfill: every vendor_group_id already on a vendor role becomes a group with
-- the deterministic code 'VG' + the first 8 hex digits of its id. When that
-- code is taken by another group of the tenant (a prefix collision), the next
-- shape is tried: 'V' + 9 hex, 10 hex, 'G' + hex digits 9 to 17. Groups are
-- visited in tenant and id order, so the same data always gets the same codes.
-- The statement reads every tenant, so it needs a role that bypasses row-level
-- security (FORCE stays on). Under a role without the bypass (passage_app) it
-- would see no rows: it is skipped with a notice, and the groups are registered
-- through POST /v1/party/vendor-groups with their existing id instead.
DO $$
DECLARE
  r record;
  h text;
  candidate text;
  chosen text;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND (rolsuper OR rolbypassrls)) THEN
    RAISE NOTICE 'vendor group code backfill skipped: role % does not bypass row-level security', current_user;
    RETURN;
  END IF;
  FOR r IN SELECT DISTINCT tenant_id, vendor_group_id AS id FROM vendor_role ORDER BY tenant_id, vendor_group_id LOOP
    CONTINUE WHEN EXISTS (SELECT 1 FROM vendor_group g WHERE g.tenant_id = r.tenant_id AND g.id = r.id);
    h := upper(replace(r.id::text, '-', ''));
    chosen := NULL;
    FOREACH candidate IN ARRAY ARRAY['VG' || left(h, 8), 'V' || left(h, 9), left(h, 10), 'G' || substr(h, 9, 9)] LOOP
      IF NOT EXISTS (SELECT 1 FROM vendor_group g WHERE g.tenant_id = r.tenant_id AND g.code = candidate) THEN
        chosen := candidate;
        EXIT;
      END IF;
    END LOOP;
    IF chosen IS NULL THEN
      RAISE EXCEPTION 'no free vendor group code for %', r.id;
    END IF;
    INSERT INTO vendor_group (tenant_id, id, code, name) VALUES (r.tenant_id, r.id, chosen, 'Vendor group ' || chosen);
  END LOOP;
END
$$;
