-- User-defined validation rules (FR-11.21, AC-11.14). A rule is a prerequisite and a
-- check, each a list of conditions, with an error or warning message. Rules are edited
-- in place; lock_version counts the changes and is the entity tag.
CREATE TABLE IF NOT EXISTS validation_rule (
  tenant_id       uuid        NOT NULL,
  id              uuid        NOT NULL,
  legal_entity_id uuid        NOT NULL,
  ledger_id       uuid        NOT NULL,
  sequence        int         NOT NULL CHECK (sequence BETWEEN 1 AND 9999),
  name            text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
  level           text        NOT NULL CHECK (level IN ('line', 'header')),
  severity        text        NOT NULL CHECK (severity IN ('error', 'warning')),
  prerequisite    jsonb       NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(prerequisite) = 'array' AND jsonb_array_length(prerequisite) <= 10),
  checks          jsonb       NOT NULL CHECK (jsonb_typeof(checks) = 'array' AND jsonb_array_length(checks) BETWEEN 1 AND 10),
  message         text        NOT NULL CHECK (char_length(message) BETWEEN 1 AND 200),
  active          boolean     NOT NULL DEFAULT true,
  lock_version    int         NOT NULL DEFAULT 1,
  created_by      text        NOT NULL DEFAULT '',
  updated_by      text        NOT NULL DEFAULT '',
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, ledger_id, name),
  FOREIGN KEY (tenant_id, ledger_id) REFERENCES ledger (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS validation_rule_ledger_sequence ON validation_rule (tenant_id, ledger_id, sequence, name);

ALTER TABLE validation_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE validation_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY validation_rule_tenant_access ON validation_rule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY validation_rule_legal_entity_scope ON validation_rule AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Rules are inserted and updated in place (activate, deactivate); never deleted.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON validation_rule TO passage_app';
  END IF;
END
$$;
