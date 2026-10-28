ALTER TABLE ledger_set DROP CONSTRAINT IF EXISTS ledger_set_tenant_id_code_key;
CREATE UNIQUE INDEX IF NOT EXISTS ledger_set_tenant_entity_code_uk
    ON ledger_set (tenant_id, legal_entity_id, code);
