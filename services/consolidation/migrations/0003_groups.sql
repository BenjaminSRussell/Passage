-- Wave 40 (#40-1, #40-2): the consolidation group structure (FR-40.7) and the
-- collection of unit trial balances (FR-40.8). A group has units (one per
-- legal entity with the ledger to collect from and its functional currency),
-- effective-dated ownership per unit and holder, and the chart mapping from
-- main accounts to consolidation accounts. Ownership history is kept: a
-- change closes the open interval instead of editing it. A collection is the
-- immutable snapshot of what the ledgers held for a period, mapped and
-- translated; the consolidated trial balance is read from it. Every table
-- carries tenant_id and is tenant-scoped by row-level security.

CREATE TABLE IF NOT EXISTS consolidation_group (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Za-z0-9._:-]{1,64}$'),
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  created_by text NOT NULL CHECK (length(created_by) >= 1),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);

CREATE TABLE IF NOT EXISTS consolidation_unit (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  group_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  ledger_id text NOT NULL CHECK (length(ledger_id) BETWEEN 1 AND 64),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, group_id, legal_entity_id),
  FOREIGN KEY (tenant_id, group_id) REFERENCES consolidation_group (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS consolidation_ownership (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  unit_id uuid NOT NULL,
  holder_unit_id uuid,
  effective_from date NOT NULL,
  effective_to date,
  percent numeric(7,4) NOT NULL CHECK (percent >= 0 AND percent <= 100),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK (effective_to IS NULL OR effective_to > effective_from),
  FOREIGN KEY (tenant_id, unit_id) REFERENCES consolidation_unit (tenant_id, id),
  FOREIGN KEY (tenant_id, holder_unit_id) REFERENCES consolidation_unit (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS consolidation_ownership_start
  ON consolidation_ownership (tenant_id, unit_id, coalesce(holder_unit_id, '00000000-0000-0000-0000-000000000000'::uuid), effective_from);

CREATE TABLE IF NOT EXISTS consolidation_account_map (
  tenant_id uuid NOT NULL,
  group_id uuid NOT NULL,
  main_account text NOT NULL CHECK (main_account ~ '^[A-Za-z0-9._:-]{1,64}$'),
  consolidation_account text NOT NULL CHECK (consolidation_account ~ '^[A-Za-z0-9._:-]{1,64}$'),
  account_type text NOT NULL CHECK (account_type IN ('balance_sheet', 'income')),
  PRIMARY KEY (tenant_id, group_id, main_account),
  FOREIGN KEY (tenant_id, group_id) REFERENCES consolidation_group (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS consolidation_collection (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  group_id uuid NOT NULL,
  period text NOT NULL CHECK (period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  version integer NOT NULL CHECK (version >= 1),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  inputs jsonb NOT NULL,
  created_by text NOT NULL CHECK (length(created_by) >= 1),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, group_id, period, version),
  FOREIGN KEY (tenant_id, group_id) REFERENCES consolidation_group (tenant_id, id)
);

-- One row per unit and consolidation account: the ledger amount in the unit's
-- currency (debit minus credit), the rate applied and the amount in the group
-- currency. Accounts without a mapping are kept with consolidation_account
-- NULL so the collection shows what was not mapped.
CREATE TABLE IF NOT EXISTS consolidation_collection_line (
  tenant_id uuid NOT NULL,
  collection_id uuid NOT NULL,
  seq integer NOT NULL,
  unit_id uuid NOT NULL,
  main_account text NOT NULL,
  consolidation_account text,
  account_type text,
  unit_currency text NOT NULL CHECK (unit_currency ~ '^[A-Z]{3}$'),
  unit_amount numeric(28,6) NOT NULL,
  rate numeric(20,10) NOT NULL CHECK (rate > 0),
  group_amount numeric(28,6) NOT NULL,
  PRIMARY KEY (tenant_id, collection_id, seq),
  FOREIGN KEY (tenant_id, collection_id) REFERENCES consolidation_collection (tenant_id, id)
);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['consolidation_collection', 'consolidation_collection_line'] LOOP
    EXECUTE format('CREATE TRIGGER %I_immutable BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION consolidation_reject_change()', t, t);
    EXECUTE format('CREATE TRIGGER %I_no_truncate BEFORE TRUNCATE ON %I FOR EACH STATEMENT EXECUTE FUNCTION consolidation_reject_change()', t, t);
  END LOOP;
  FOREACH t IN ARRAY ARRAY['consolidation_group', 'consolidation_unit', 'consolidation_ownership', 'consolidation_account_map',
                           'consolidation_collection', 'consolidation_collection_line'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format($p$CREATE POLICY %I_tenant_access ON %I AS PERMISSIVE
      USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
      WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid)$p$, t, t);
  END LOOP;
END
$$;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON consolidation_group, consolidation_unit, consolidation_collection, consolidation_collection_line TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON consolidation_ownership TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON consolidation_account_map TO passage_app';
  END IF;
END
$$;
