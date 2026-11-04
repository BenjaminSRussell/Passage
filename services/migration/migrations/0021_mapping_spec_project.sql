-- A mapping spec can belong to a migration project (PRD MappingSpec.project_id) and records the natural key columns of
-- its mapping (natural_key, copied from the mapping JSON so a list can show it). A spec with no project stays
-- tenant-wide, as every spec published before this migration. The (entity, version) pair is now unique per project, so
-- two projects can each publish their own version 1 of an entity; the tenant-wide specs keep one namespace.
ALTER TABLE migration.mapping_spec ADD COLUMN project_id uuid;
ALTER TABLE migration.mapping_spec ADD COLUMN natural_key text[] NOT NULL DEFAULT '{}';
ALTER TABLE migration.mapping_spec ADD CONSTRAINT mapping_spec_project_fk
  FOREIGN KEY (tenant_id, project_id) REFERENCES migration.project (tenant_id, id);

ALTER TABLE migration.mapping_spec DROP CONSTRAINT mapping_spec_tenant_id_entity_version_key;
CREATE UNIQUE INDEX mapping_spec_scope_version_idx ON migration.mapping_spec
  (tenant_id, coalesce(project_id, '00000000-0000-0000-0000-000000000000'::uuid), entity, version);
CREATE INDEX mapping_spec_project_idx ON migration.mapping_spec (tenant_id, project_id, entity, version DESC)
  WHERE project_id IS NOT NULL;
