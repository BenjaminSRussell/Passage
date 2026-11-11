-- Inventory journal numbers (#500). A journal had only its id, so pages showed a
-- short id. Every journal now gets IJ-000001, IJ-000002, ... per legal entity,
-- from a counter row the insert trigger takes under a row lock (a rolled-back
-- insert gives its number back). Every insert path (journals, copies, count
-- sheet postings, blocking scrap, opening balances) is numbered the same way.
ALTER TABLE inventory_journal ADD COLUMN IF NOT EXISTS number text;

CREATE TABLE IF NOT EXISTS inventory_journal_counter (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  last_number bigint NOT NULL CHECK (last_number > 0),
  PRIMARY KEY (tenant_id, legal_entity_id)
);
ALTER TABLE inventory_journal_counter ENABLE ROW LEVEL SECURITY;
ALTER TABLE inventory_journal_counter FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS inventory_journal_counter_tenant_access ON inventory_journal_counter;
CREATE POLICY inventory_journal_counter_tenant_access ON inventory_journal_counter AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS inventory_journal_counter_legal_entity_scope ON inventory_journal_counter;
CREATE POLICY inventory_journal_counter_legal_entity_scope ON inventory_journal_counter AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE OR REPLACE FUNCTION inventory_journal_assign_number() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  n bigint;
BEGIN
  IF NEW.number IS NULL THEN
    INSERT INTO inventory_journal_counter AS c (tenant_id, legal_entity_id, last_number) VALUES (NEW.tenant_id, NEW.legal_entity_id, 1)
      ON CONFLICT (tenant_id, legal_entity_id) DO UPDATE SET last_number = c.last_number + 1
      RETURNING last_number INTO n;
    NEW.number := 'IJ-' || lpad(n::text, 6, '0');
  END IF;
  RETURN NEW;
END
$$;
DROP TRIGGER IF EXISTS inventory_journal_number ON inventory_journal;
CREATE TRIGGER inventory_journal_number BEFORE INSERT ON inventory_journal FOR EACH ROW EXECUTE FUNCTION inventory_journal_assign_number();

-- Backfill: existing journals are numbered per legal entity in the order they
-- were created, and the counter continues after them. It reads every tenant, so
-- it runs only under a role that bypasses row-level security; under passage_app
-- it is skipped with a notice.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND (rolsuper OR rolbypassrls)) THEN
    RAISE NOTICE 'inventory journal number backfill skipped: role % does not bypass row-level security', current_user;
    RETURN;
  END IF;
  WITH numbered AS (
    SELECT tenant_id, id, row_number() OVER (PARTITION BY tenant_id, legal_entity_id ORDER BY created_at, id) AS n
    FROM inventory_journal WHERE number IS NULL
  )
  UPDATE inventory_journal j SET number = 'IJ-' || lpad(numbered.n::text, 6, '0')
  FROM numbered WHERE j.tenant_id = numbered.tenant_id AND j.id = numbered.id;
  INSERT INTO inventory_journal_counter (tenant_id, legal_entity_id, last_number)
    SELECT tenant_id, legal_entity_id, count(*) FROM inventory_journal GROUP BY tenant_id, legal_entity_id
    ON CONFLICT (tenant_id, legal_entity_id) DO UPDATE SET last_number = greatest(inventory_journal_counter.last_number, excluded.last_number);
END
$$;

CREATE UNIQUE INDEX IF NOT EXISTS inventory_journal_number_key ON inventory_journal (tenant_id, legal_entity_id, number) WHERE number IS NOT NULL;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON inventory_journal_counter TO passage_app';
  END IF;
END
$$;
