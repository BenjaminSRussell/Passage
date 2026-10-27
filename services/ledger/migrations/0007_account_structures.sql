-- Account structures (FR-11.3), the financial dimension value register
-- (FR-11.2) and the structure version on dimension combinations (FR-11.4).
-- Additive only (expand/contract, Atlas).

-- Registered values of a ledger's dimension attributes, effective-dated and
-- suspendable. Values that are not registered are format-checked only.
CREATE TABLE IF NOT EXISTS dimension_value (
  tenant_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  attribute text NOT NULL CHECK (attribute ~ '^[A-Z][A-Z0-9_]{0,19}$'),
  value text NOT NULL CHECK (value ~ '^[A-Za-z0-9._-]{1,20}$'),
  name text NOT NULL DEFAULT '' CHECK (char_length(name) <= 60),
  active_from date,
  active_to date,
  suspended boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, ledger_id, attribute, value),
  FOREIGN KEY (tenant_id, ledger_id) REFERENCES ledger (tenant_id, id),
  CHECK (active_to IS NULL OR active_from IS NULL OR active_to >= active_from)
);
ALTER TABLE dimension_value ENABLE ROW LEVEL SECURITY;
ALTER TABLE dimension_value FORCE ROW LEVEL SECURITY;
CREATE POLICY dimension_value_tenant_access ON dimension_value AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Account structure versions. A draft may be edited; an active version may
-- only be retired and a retired one is final (the store enforces this). A
-- posting uses, per structure name, the active version in effect on the
-- posting date, trying structures by priority then name; the first whose
-- main-account filter admits the account governs the line.
CREATE TABLE IF NOT EXISTS account_structure (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  name text NOT NULL CHECK (name ~ '^[A-Za-z0-9_.-]{1,60}$'),
  version int NOT NULL CHECK (version >= 1),
  priority int NOT NULL DEFAULT 0 CHECK (priority >= 0),
  effective_from date NOT NULL,
  status text NOT NULL CHECK (status IN ('draft', 'active', 'retired')),
  segments text[] NOT NULL DEFAULT '{}' CHECK (cardinality(segments) <= 20),
  account_filter text[] NOT NULL DEFAULT '{}',
  constraints jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(constraints) = 'array'),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, ledger_id, name, version),
  FOREIGN KEY (tenant_id, ledger_id) REFERENCES ledger (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS account_structure_active ON account_structure (tenant_id, ledger_id) WHERE status = 'active';
ALTER TABLE account_structure ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_structure FORCE ROW LEVEL SECURITY;
CREATE POLICY account_structure_tenant_access ON account_structure AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- The structure version a combination was validated under (FR-11.4). Null for
-- combinations of ledgers without structures. Combinations stay immutable.
ALTER TABLE dimension_combination ADD COLUMN IF NOT EXISTS account_structure_id uuid;

-- The app role needs the new tables (0005_grants.sql cannot cover them).
-- Skipped when the role does not exist, as in 0005_grants.sql.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON account_structure, dimension_value TO passage_app';
  END IF;
END
$$;
