-- Backfill of migration.xref.legal_entity_id (0013) for the rows earlier runs loaded: only what the stored data proves.
-- A record stays in the legal entity it was created in (the party service has no import route that changes a customer
-- or vendor role, and an open item's import key is unique per tenant, so a load into another legal entity is refused),
-- so a committed load of the key (a created or updated ledger line of a non-dry run) under a project with a target
-- legal entity proves its legal entity. A key whose loads sit under projects of two legal entities is ambiguous and
-- stays NULL, as do keys loaded only by runs outside such a project, keys with no load line and every row of a
-- tenant-wide entity. Unchanged lines prove nothing (a run under another project skips a row it did not load).
--
-- The statement reads across tenants, so it needs the RLS bypass the migration role already has for
-- migration.sync_candidates (0011); FORCE ROW LEVEL SECURITY is left on, so no table lock is taken: the update holds
-- the row locks of the rows it tags until the migration commits, and reads of migration.xref go on meanwhile. A role
-- without the bypass would see no rows, so the backfill is skipped with a notice and the rows stay NULL until a load,
-- a rerun of a row that names its legal entity, or xref:record tags them.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND (rolsuper OR rolbypassrls)) THEN
    RAISE NOTICE 'migration.xref legal entity backfill skipped: role % does not bypass row-level security', current_user;
    RETURN;
  END IF;
  UPDATE migration.xref x SET legal_entity_id = d.legal_entity_id
  FROM (
    SELECT x2.tenant_id, x2.source_system, x2.source_entity, x2.source_key, min(pr.target_legal_entity_id::text)::uuid AS legal_entity_id
    FROM migration.xref x2
    JOIN migration.run_ledger l ON l.tenant_id = x2.tenant_id AND l.source_key = x2.source_key AND l.target_id = x2.target_id
                               AND l.action IN ('created', 'updated')
    JOIN migration.run r ON r.tenant_id = l.tenant_id AND r.id = l.run_id AND r.source_system = x2.source_system
                        AND r.entity = x2.source_entity AND NOT r.dry_run
    JOIN migration.project pr ON pr.tenant_id = r.tenant_id AND pr.id = r.project_id AND pr.target_legal_entity_id IS NOT NULL
    WHERE x2.legal_entity_id IS NULL AND x2.source_entity IN ('customer', 'vendor', 'ar_open_item')
    GROUP BY x2.tenant_id, x2.source_system, x2.source_entity, x2.source_key
    HAVING count(DISTINCT pr.target_legal_entity_id) = 1
  ) d
  WHERE x.tenant_id = d.tenant_id AND x.source_system = d.source_system AND x.source_entity = d.source_entity
    AND x.source_key = d.source_key AND x.legal_entity_id IS NULL;
END
$$;
