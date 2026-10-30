-- Ledger-bound tables carry the owning legal entity (#241). The column is
-- nullable so existing rows stay valid: a NULL entity matches no scoped caller
-- and is visible only to all-entity callers until it is assigned.
ALTER TABLE statement_run ADD COLUMN IF NOT EXISTS legal_entity_id uuid;
ALTER TABLE reporting_balance ADD COLUMN IF NOT EXISTS legal_entity_id uuid;
ALTER TABLE reporting_hwm ADD COLUMN IF NOT EXISTS legal_entity_id uuid;
ALTER TABLE reporting_journal_line ADD COLUMN IF NOT EXISTS legal_entity_id uuid;
ALTER TABLE audit_trail_entry ADD COLUMN IF NOT EXISTS legal_entity_id uuid;

DO $$
DECLARE
  parent_table text;
BEGIN
  FOREACH parent_table IN ARRAY ARRAY['statement_run', 'reporting_balance', 'reporting_hwm', 'reporting_journal_line']
  LOOP
    EXECUTE format(
      'CREATE POLICY %I ON %I AS RESTRICTIVE
         USING (current_setting(''passage.legal_entity_scopes'', true) = ''*''
                OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))',
      parent_table || '_legal_entity_scope', parent_table
    );
  END LOOP;
END $$;

-- Entries about reporting objects (definitions, hierarchies, schedules) have no
-- ledger and stay tenant-level; ledger entries follow their legal entity.
CREATE POLICY audit_trail_entry_legal_entity_scope ON audit_trail_entry AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR ledger_id IS NULL
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Cells inherit the scope of their run.
CREATE POLICY statement_cell_legal_entity_scope ON statement_cell AS RESTRICTIVE
  USING (
    current_setting('passage.legal_entity_scopes', true) = '*'
    OR EXISTS (
      SELECT 1 FROM statement_run AS scope_parent
      WHERE scope_parent.tenant_id = statement_cell.tenant_id
        AND scope_parent.id = statement_cell.run_id
        AND scope_parent.legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ','))
    )
  ) WITH CHECK (
    current_setting('passage.legal_entity_scopes', true) = '*'
    OR EXISTS (
      SELECT 1 FROM statement_run AS scope_parent
      WHERE scope_parent.tenant_id = statement_cell.tenant_id
        AND scope_parent.id = statement_cell.run_id
        AND scope_parent.legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ','))
    )
  );
