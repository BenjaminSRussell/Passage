CREATE TABLE IF NOT EXISTS posting_account (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  inventory_account text NOT NULL CHECK (inventory_account <> ''),
  cost_change_account text NOT NULL CHECK (cost_change_account <> '' AND cost_change_account <> inventory_account),
  updated_by text NOT NULL DEFAULT '',
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id)
);
ALTER TABLE posting_account ENABLE ROW LEVEL SECURITY;
ALTER TABLE posting_account FORCE ROW LEVEL SECURITY;
CREATE POLICY posting_account_tenant_access ON posting_account AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY posting_account_legal_entity_scope ON posting_account AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON posting_account TO passage_app';
  END IF;
END
$$;
