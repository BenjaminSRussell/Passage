-- Document splitting (FR-11.23, BR-11.15, AC-11.16): per ledger and source type, the dimensions
-- that lines without a value inherit from the lines that carry them, and the account that takes
-- the zero-balance clearing lines. version counts replacements and is the entity tag.
CREATE TABLE IF NOT EXISTS splitting_rule (
  tenant_id                    uuid        NOT NULL,
  id                           uuid        NOT NULL,
  legal_entity_id              uuid        NOT NULL,
  ledger_id                    uuid        NOT NULL,
  source_type                  text        NOT NULL CHECK (char_length(source_type) BETWEEN 1 AND 60),
  splitting_dimensions         text[]      NOT NULL CHECK (cardinality(splitting_dimensions) BETWEEN 1 AND 5),
  zero_balance_clearing_account text,
  default_splitting_value      jsonb       NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(default_splitting_value) = 'object'),
  active                       boolean     NOT NULL DEFAULT true,
  version                      int         NOT NULL DEFAULT 1,
  created_by                   text        NOT NULL DEFAULT '',
  created_at                   timestamptz NOT NULL DEFAULT now(),
  updated_at                   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, ledger_id, source_type),
  FOREIGN KEY (tenant_id, ledger_id) REFERENCES ledger (tenant_id, id)
);

ALTER TABLE splitting_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE splitting_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY splitting_rule_tenant_access ON splitting_rule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY splitting_rule_legal_entity_scope ON splitting_rule AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Rules are replaced in place and never deleted (a rule is switched off with active = false).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON splitting_rule TO passage_app';
  END IF;
END
$$;
