-- pg_trgm lives in public, as in services/party: an unqualified CREATE EXTENSION lands in the first
-- schema of the search_path, so a migration run with another schema first (the testkit's throwaway
-- schemas) would create it there, a later run would skip it and not see gin_trgm_ops, and dropping
-- that schema would drop the extension. Two first runs at once race on pg_extension's unique index;
-- the loser finds the winner's extension once the winner commits.
DO $$
BEGIN
  CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;
EXCEPTION WHEN unique_violation THEN
  NULL;
END
$$;
CREATE INDEX IF NOT EXISTS postal_address_formatted_trgm ON postal_address USING gin (formatted public.gin_trgm_ops);
