CREATE TABLE IF NOT EXISTS ledger_set (
    tenant_id         uuid        NOT NULL,
    id                uuid        NOT NULL,
    legal_entity_id   uuid        NOT NULL,
    code              text        NOT NULL CHECK (code ~ '^[A-Z0-9]{1,10}$'),
    name              text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
    leading_ledger_id uuid        NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, code),
    FOREIGN KEY (tenant_id, leading_ledger_id) REFERENCES ledger (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS ledger_set_member (
    tenant_id       uuid NOT NULL,
    ledger_set_id   uuid NOT NULL,
    ledger_id       uuid NOT NULL,
    legal_entity_id uuid NOT NULL,
    PRIMARY KEY (tenant_id, ledger_set_id, ledger_id),
    FOREIGN KEY (tenant_id, ledger_set_id) REFERENCES ledger_set (tenant_id, id),
    FOREIGN KEY (tenant_id, ledger_id) REFERENCES ledger (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS ledger_account_mapping (
    tenant_id       uuid        NOT NULL,
    id              uuid        NOT NULL,
    legal_entity_id uuid        NOT NULL,
    ledger_id       uuid        NOT NULL,
    source_account  text        NOT NULL CHECK (source_account ~ '^[A-Za-z0-9._-]{1,20}$'),
    target_account  text        NOT NULL CHECK (target_account ~ '^[A-Za-z0-9._-]{1,20}$'),
    effective_from  date        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, ledger_id, source_account, effective_from),
    CHECK (source_account <> target_account),
    FOREIGN KEY (tenant_id, ledger_id) REFERENCES ledger (tenant_id, id)
);

ALTER TABLE ledger_set ENABLE ROW LEVEL SECURITY;
ALTER TABLE ledger_set FORCE ROW LEVEL SECURITY;
CREATE POLICY ledger_set_tenant_access ON ledger_set AS PERMISSIVE
    USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY ledger_set_legal_entity_scope ON ledger_set AS RESTRICTIVE
    USING (current_setting('passage.legal_entity_scopes', true) = '*'
           OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))
    WITH CHECK (current_setting('passage.legal_entity_scopes', true) = '*'
           OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE ledger_set_member ENABLE ROW LEVEL SECURITY;
ALTER TABLE ledger_set_member FORCE ROW LEVEL SECURITY;
CREATE POLICY ledger_set_member_tenant_access ON ledger_set_member AS PERMISSIVE
    USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY ledger_set_member_legal_entity_scope ON ledger_set_member AS RESTRICTIVE
    USING (current_setting('passage.legal_entity_scopes', true) = '*'
           OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))
    WITH CHECK (current_setting('passage.legal_entity_scopes', true) = '*'
           OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE ledger_account_mapping ENABLE ROW LEVEL SECURITY;
ALTER TABLE ledger_account_mapping FORCE ROW LEVEL SECURITY;
CREATE POLICY ledger_account_mapping_tenant_access ON ledger_account_mapping AS PERMISSIVE
    USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY ledger_account_mapping_legal_entity_scope ON ledger_account_mapping AS RESTRICTIVE
    USING (current_setting('passage.legal_entity_scopes', true) = '*'
           OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))
    WITH CHECK (current_setting('passage.legal_entity_scopes', true) = '*'
           OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'REVOKE UPDATE, DELETE, TRUNCATE ON ledger_set, ledger_set_member, ledger_account_mapping FROM passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON ledger_set, ledger_set_member, ledger_account_mapping TO passage_app';
  END IF;
END
$$;
