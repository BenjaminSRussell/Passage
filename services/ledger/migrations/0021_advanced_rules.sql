-- Advanced rule structures (FR-11.16, AC-11.18). An advanced rule belongs to one account
-- structure version and so shares its activation: it is edited while the structure is a
-- draft and governs postings once the structure is active. When a line matches the rule's
-- filter (main account tokens and dimension tokens), the rule's segments and constraint rows
-- are appended to the structure applied to that line, in priority order. lock_version counts
-- the changes and is the entity tag.
CREATE TABLE IF NOT EXISTS advanced_rule (
  tenant_id       uuid        NOT NULL,
  id              uuid        NOT NULL,
  legal_entity_id uuid        NOT NULL,
  ledger_id       uuid        NOT NULL,
  structure_id    uuid        NOT NULL,
  name            text        NOT NULL CHECK (name ~ '^[A-Za-z0-9_.-]{1,60}$'),
  priority        int         NOT NULL DEFAULT 0 CHECK (priority >= 0),
  accounts        text[]      NOT NULL DEFAULT '{}',
  dimensions      jsonb       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(dimensions) = 'object'),
  segments        text[]      NOT NULL CHECK (cardinality(segments) BETWEEN 1 AND 20),
  constraints     jsonb       NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(constraints) = 'array'),
  lock_version    int         NOT NULL DEFAULT 1,
  created_by      text        NOT NULL DEFAULT '',
  updated_by      text        NOT NULL DEFAULT '',
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, structure_id, name),
  FOREIGN KEY (tenant_id, ledger_id) REFERENCES ledger (tenant_id, id),
  FOREIGN KEY (tenant_id, structure_id) REFERENCES account_structure (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS advanced_rule_structure ON advanced_rule (tenant_id, ledger_id, structure_id, priority, name);

ALTER TABLE advanced_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE advanced_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY advanced_rule_tenant_access ON advanced_rule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY advanced_rule_legal_entity_scope ON advanced_rule AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Rules are inserted and updated in place while the structure is a draft; never deleted.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON advanced_rule TO passage_app';
  END IF;
END
$$;
