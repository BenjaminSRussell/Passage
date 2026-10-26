CREATE TABLE IF NOT EXISTS party_merge (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  survivor_party_id uuid NOT NULL,
  merged_party_id uuid NOT NULL,
  field_resolution jsonb NOT NULL,
  roles_moved integer NOT NULL CHECK (roles_moved >= 0),
  references_rewritten integer NOT NULL CHECK (references_rewritten >= 0),
  merged_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, merged_party_id),
  FOREIGN KEY (tenant_id, survivor_party_id) REFERENCES party (tenant_id, id),
  FOREIGN KEY (tenant_id, merged_party_id) REFERENCES party (tenant_id, id),
  CHECK (survivor_party_id <> merged_party_id)
);

CREATE INDEX IF NOT EXISTS customer_role_invoice_account_party
  ON customer_role (tenant_id, invoice_account_party_id) WHERE invoice_account_party_id IS NOT NULL;

ALTER TABLE party_merge ENABLE ROW LEVEL SECURITY;
ALTER TABLE party_merge FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS party_merge_tenant_access ON party_merge;
CREATE POLICY party_merge_tenant_access ON party_merge AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
