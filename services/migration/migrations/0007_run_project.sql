-- Runs belong to a migration project (BR-10.2, FR-10.14). project_id is optional: runs started without a project
-- keep working. A project carries a claim while one of its runs executes so a second start is refused (409
-- MIG-RUN-ACTIVE): active_run_id is the id the running run's row will get when it finishes (also the claim's token,
-- so a run releases only its own claim) and active_run_started_at is when the claim was taken, on the database
-- clock. The claim is cleared when the run ends and treated as abandoned after the run deadline plus a margin, so a
-- crashed process cannot block a project for good. No new table: migration.run already grants INSERT and
-- migration.project UPDATE to passage_app.
ALTER TABLE migration.run ADD COLUMN project_id uuid;
ALTER TABLE migration.run ADD CONSTRAINT run_project_fk
  FOREIGN KEY (tenant_id, project_id) REFERENCES migration.project (tenant_id, id);
CREATE INDEX run_project_idx ON migration.run (tenant_id, project_id, created_at DESC, id DESC) WHERE project_id IS NOT NULL;

ALTER TABLE migration.project ADD COLUMN active_run_id uuid;
ALTER TABLE migration.project ADD COLUMN active_run_started_at timestamptz;
ALTER TABLE migration.project ADD CONSTRAINT project_active_run_claim
  CHECK ((active_run_id IS NULL) = (active_run_started_at IS NULL));
