-- A company's opening-journal target is released when the first posting is refused and nothing was booked
-- (#549). The store deletes a pin only while the company has no posting row, so a pin with postings stays
-- as the record of where the journals went.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT DELETE ON gl_opening_target TO passage_app';
  END IF;
END
$$;
