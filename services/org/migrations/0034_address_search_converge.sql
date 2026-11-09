-- Converges databases that applied 0026 before it was edited in place (dbx.Migrate tracks
-- migrations by file name, with no checksum, so those databases still carry the original
-- unqualified form). On a database that ran the current 0026 this changes nothing.
--
-- pg_trgm: installed in public when absent; if it already lives in another schema (the original
-- 0026 put it in the first schema of the search_path) it is left where it is. The extension is
-- trusted, so the non-superuser database owner may create it; it is only created when missing.
--
-- The trigram index from 0026 is recreated only when it is missing or invalid, with the operator
-- class taken from whichever schema holds the extension. The rebuild is a plain CREATE INDEX inside
-- the migration transaction (it blocks writes to postal_address while it builds), which is only
-- reached on a database that lost the index.
DO $$
DECLARE
  ext_schema text;
  idx regclass;
  usable boolean;
BEGIN
  SELECT n.nspname INTO ext_schema
    FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace
   WHERE e.extname = 'pg_trgm';
  IF ext_schema IS NULL THEN
    BEGIN
      CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;
    EXCEPTION WHEN unique_violation THEN
      NULL;
    END;
    SELECT n.nspname INTO ext_schema
      FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace
     WHERE e.extname = 'pg_trgm';
  END IF;

  -- Look the index up on the postal_address table this migration sees, not by name through the
  -- search_path, so a same-named index in another schema is never taken for this one.
  SELECT i.indexrelid::regclass, i.indisvalid INTO idx, usable
    FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
   WHERE i.indrelid = 'postal_address'::regclass AND c.relname = 'postal_address_formatted_trgm';
  IF idx IS NOT NULL THEN
    IF usable THEN
      RETURN;
    END IF;
    EXECUTE 'DROP INDEX ' || idx::text;
  END IF;
  EXECUTE format('CREATE INDEX postal_address_formatted_trgm ON postal_address USING gin (formatted %I.gin_trgm_ops)', ext_schema);
END
$$;
