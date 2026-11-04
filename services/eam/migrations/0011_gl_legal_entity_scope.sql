-- #486: the posting profile and the GL subledger are read and written only for a legal entity in the caller's scope
-- (glScope), so RLS enforces the same scope as eam_pending_booking. asset and work_order stay tenant-wide: see tools/rlscheck.
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['eam_posting_profile', 'eam_gl_posting'] LOOP
    EXECUTE format('DROP POLICY IF EXISTS %I_legal_entity_scope ON %I', t, t);
    EXECUTE format('CREATE POLICY %I_legal_entity_scope ON %I AS RESTRICTIVE
      USING (current_setting(''passage.legal_entity_scopes'', true) = ''*''
             OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t, t);
  END LOOP;
END $$;
