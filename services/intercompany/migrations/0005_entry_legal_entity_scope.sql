-- A caller sees and writes only the IC journal entries of legal entities in its
-- scope. The API refuses a posting with a side outside the scope (AUTH-0003)
-- before anything is stored; this policy keeps reads to the caller's own sides.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename = 'ic_journal_entry' AND policyname = 'ic_journal_entry_legal_entity_scope') THEN
    CREATE POLICY ic_journal_entry_legal_entity_scope ON ic_journal_entry AS RESTRICTIVE
      USING (current_setting('passage.legal_entity_scopes', true) = '*'
             OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
  END IF;
END $$;
