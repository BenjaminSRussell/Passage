CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE INDEX IF NOT EXISTS postal_address_formatted_trgm ON postal_address USING gin (formatted gin_trgm_ops);
