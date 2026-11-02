-- The loader (integration track S3) updates records in the owning services under optimistic versioning, so the
-- cross-reference keeps the version each target answered at the last load (0 for a target that keeps none). No new
-- table, so no grants.
ALTER TABLE migration.xref ADD COLUMN target_version integer NOT NULL DEFAULT 0 CHECK (target_version >= 0);
