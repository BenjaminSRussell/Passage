-- Dimension derivation strategies and lookup tables (FR-11.22, BR-11.14, AC-11.15).

-- Lookup tables map a source tuple (ACCOUNT, ACCOUNT_TYPE or dimension attributes) to one target
-- dimension value. The rows are replaced as a whole by the maintenance call; version counts replacements and is the entity tag.
CREATE TABLE IF NOT EXISTS lookup_table (
  tenant_id       uuid        NOT NULL,
  id              uuid        NOT NULL,
  legal_entity_id uuid        NOT NULL,
  ledger_id       uuid        NOT NULL,
  name            text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
  source_fields   text[]      NOT NULL CHECK (cardinality(source_fields) BETWEEN 1 AND 5),
  target          text        NOT NULL CHECK (target ~ '^[A-Z][A-Z0-9_]{0,19}$'),
  version         int         NOT NULL DEFAULT 1,
  created_by      text        NOT NULL DEFAULT '',
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, ledger_id, name),
  FOREIGN KEY (tenant_id, ledger_id) REFERENCES ledger (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS lookup_table_row (
  tenant_id       uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  lookup_table_id uuid NOT NULL,
  row_no          int  NOT NULL CHECK (row_no >= 1),
  source_values   text[] NOT NULL CHECK (cardinality(source_values) BETWEEN 1 AND 5),
  target_value    text NOT NULL CHECK (char_length(target_value) BETWEEN 1 AND 20),
  valid_from      date NOT NULL,
  valid_to        date,
  PRIMARY KEY (tenant_id, lookup_table_id, row_no),
  UNIQUE (tenant_id, lookup_table_id, source_values, valid_from),
  CHECK (valid_to IS NULL OR valid_to >= valid_from),
  FOREIGN KEY (tenant_id, lookup_table_id) REFERENCES lookup_table (tenant_id, id)
);

-- Versions of a ledger's derivation strategy: draft, then active (one per ledger), then retired.
-- The steps are stored with the version so an activated version never changes.
CREATE TABLE IF NOT EXISTS derivation_strategy (
  tenant_id       uuid        NOT NULL,
  id              uuid        NOT NULL,
  legal_entity_id uuid        NOT NULL,
  ledger_id       uuid        NOT NULL,
  version         int         NOT NULL CHECK (version >= 1),
  lock_version    int         NOT NULL DEFAULT 1,
  name            text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
  status          text        NOT NULL CHECK (status IN ('draft', 'active', 'retired')),
  steps           jsonb       NOT NULL CHECK (jsonb_typeof(steps) = 'array' AND jsonb_array_length(steps) BETWEEN 1 AND 50),
  created_by      text        NOT NULL DEFAULT '',
  activated_by    text,
  activated_at    timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, ledger_id, version),
  FOREIGN KEY (tenant_id, ledger_id) REFERENCES ledger (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS derivation_strategy_one_active ON derivation_strategy (tenant_id, ledger_id) WHERE status = 'active';

-- The step ids that filled dimensions on a posted line (derivation trace). Posted lines stay append-only.
ALTER TABLE journal_line ADD COLUMN IF NOT EXISTS derived_by_step_ids uuid[] NOT NULL DEFAULT '{}';

ALTER TABLE lookup_table ENABLE ROW LEVEL SECURITY;
ALTER TABLE lookup_table FORCE ROW LEVEL SECURITY;
CREATE POLICY lookup_table_tenant_access ON lookup_table AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY lookup_table_legal_entity_scope ON lookup_table AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE lookup_table_row ENABLE ROW LEVEL SECURITY;
ALTER TABLE lookup_table_row FORCE ROW LEVEL SECURITY;
CREATE POLICY lookup_table_row_tenant_access ON lookup_table_row AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY lookup_table_row_legal_entity_scope ON lookup_table_row AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE derivation_strategy ENABLE ROW LEVEL SECURITY;
ALTER TABLE derivation_strategy FORCE ROW LEVEL SECURITY;
CREATE POLICY derivation_strategy_tenant_access ON derivation_strategy AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY derivation_strategy_legal_entity_scope ON derivation_strategy AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Lookup tables and strategy drafts are updated in place; a table's rows are replaced (deleted and inserted).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON lookup_table, derivation_strategy TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON lookup_table_row TO passage_app';
  END IF;
END
$$;
