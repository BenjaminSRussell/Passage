ALTER TABLE receiving_pending_posting
  ADD COLUMN IF NOT EXISTS legal_entity_id uuid GENERATED ALWAYS AS ((journal->>'legalEntityId')::uuid) STORED;

ALTER TABLE receiving_pending_posting_subject ADD COLUMN IF NOT EXISTS legal_entity_id uuid;

UPDATE receiving_pending_posting_subject s SET legal_entity_id = p.legal_entity_id
  FROM receiving_pending_posting p
  WHERE p.tenant_id = s.tenant_id AND p.ledger_key = s.ledger_key AND s.legal_entity_id IS NULL;

DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['receiving_pending_posting', 'receiving_pending_posting_subject'] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = t AND policyname = t || '_legal_entity_scope') THEN
      EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
    END IF;
  END LOOP;
END
$$;
