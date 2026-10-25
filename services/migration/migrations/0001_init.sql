CREATE SCHEMA IF NOT EXISTS migration;

CREATE TABLE migration.run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  source_system text NOT NULL CHECK (source_system IN ('ax2012', 'd365fo', 'sap_ecc', 'sap_s4')),
  mode text NOT NULL CHECK (mode IN ('rehearsal', 'cutover', 'delta')),
  entity text NOT NULL,
  status text NOT NULL CHECK (status IN ('succeeded', 'partial', 'failed')),
  dry_run boolean NOT NULL,
  mapping_version integer NOT NULL,
  mapping jsonb NOT NULL,
  parent_run_id uuid,
  report jsonb NOT NULL,
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);

CREATE TABLE migration.xref (
  tenant_id uuid NOT NULL,
  source_system text NOT NULL CHECK (source_system IN ('ax2012', 'd365fo', 'sap_ecc', 'sap_s4')),
  source_entity text NOT NULL,
  source_key text NOT NULL,
  target_id uuid NOT NULL,
  content_hash text NOT NULL DEFAULT '',
  merged_into_target_id uuid,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, source_system, source_entity, source_key)
);

CREATE TABLE migration.run_ledger (
  tenant_id uuid NOT NULL,
  run_id uuid NOT NULL,
  source_key text NOT NULL,
  target_id uuid,
  idempotency_key text NOT NULL,
  content_hash text NOT NULL,
  action text NOT NULL CHECK (action IN ('created', 'updated', 'unchanged')),
  PRIMARY KEY (tenant_id, run_id, source_key),
  FOREIGN KEY (tenant_id, run_id) REFERENCES migration.run (tenant_id, id)
);

ALTER TABLE migration.run ENABLE ROW LEVEL SECURITY;
ALTER TABLE migration.run FORCE ROW LEVEL SECURITY;
CREATE POLICY run_tenant_access ON migration.run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY run_legal_entity_scope ON migration.run AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*');

ALTER TABLE migration.xref ENABLE ROW LEVEL SECURITY;
ALTER TABLE migration.xref FORCE ROW LEVEL SECURITY;
CREATE POLICY xref_tenant_access ON migration.xref AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY xref_legal_entity_scope ON migration.xref AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*');

ALTER TABLE migration.run_ledger ENABLE ROW LEVEL SECURITY;
ALTER TABLE migration.run_ledger FORCE ROW LEVEL SECURITY;
CREATE POLICY run_ledger_tenant_access ON migration.run_ledger AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY run_ledger_legal_entity_scope ON migration.run_ledger AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*');
