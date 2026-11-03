-- Statistical quantity totals in the balance projection (BR-11.2). A quantity has a unit of
-- measure and quantities of different units never add up, so the totals live in their own
-- projection keyed by the balance key plus the unit. Like balance it is upserted in the
-- posting transaction and rebuilt from the journal by the rebuild endpoint. Existing
-- quantity lines (slice 18) are folded in by the next rebuild of each ledger: a migration runs
-- without a tenant setting, so row-level security would hide every journal line from a
-- backfill here.
CREATE TABLE IF NOT EXISTS balance_quantity (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  main_account_id uuid NOT NULL,
  dimension_combination_id bytea NOT NULL,
  period_key text NOT NULL CHECK (period_key ~ '^[0-9]{4}-[0-9]{2}$'),
  unit text NOT NULL CHECK (unit ~ '^[A-Z0-9]{1,10}$'),
  debit_quantity numeric(24,6) NOT NULL DEFAULT 0,
  credit_quantity numeric(24,6) NOT NULL DEFAULT 0,
  net_quantity numeric(24,6) NOT NULL DEFAULT 0,
  line_count bigint NOT NULL DEFAULT 0 CHECK (line_count >= 0),
  PRIMARY KEY (tenant_id, ledger_id, main_account_id, dimension_combination_id, period_key, unit),
  FOREIGN KEY (tenant_id, ledger_id) REFERENCES ledger (tenant_id, id),
  FOREIGN KEY (tenant_id, main_account_id) REFERENCES main_account (tenant_id, id),
  FOREIGN KEY (tenant_id, dimension_combination_id) REFERENCES dimension_combination (tenant_id, id)
);
ALTER TABLE balance_quantity ENABLE ROW LEVEL SECURITY;
ALTER TABLE balance_quantity FORCE ROW LEVEL SECURITY;
CREATE POLICY balance_quantity_tenant_access ON balance_quantity AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY balance_quantity_legal_entity_scope ON balance_quantity AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Like balance (0006) the debit and credit columns carry no sign check: a negative posting
-- (storno, BR-11.16) reduces the original side, so a delta can be negative.

-- Ledgers that existed before this migration may hold quantity lines (slice 18) the new
-- projection does not have yet, and this migration cannot backfill them (row-level security
-- hides the journal from it). They are flagged until their first rebuild, so the trial
-- balance says its quantity totals are incomplete instead of showing silent zeros. Adding the
-- column with DEFAULT true fills the existing rows without an UPDATE (which row-level security
-- would also hide); ledgers created from now on start with false.
ALTER TABLE ledger ADD COLUMN IF NOT EXISTS quantity_rebuild_pending boolean NOT NULL DEFAULT true;
ALTER TABLE ledger ALTER COLUMN quantity_rebuild_pending SET DEFAULT false;

-- Upserted by posting and deleted and rewritten by the rebuild, like balance.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON balance_quantity TO passage_app';
  END IF;
END
$$;
