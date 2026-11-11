-- Knowledge index (FR-47.14): the chunks of knowledge articles, documents and tenant policies the copilot cites. The
-- keyword half of the hybrid search is Postgres full-text search over `tsv` (GIN); the embedding half is a separate
-- port (pgvector is an extension this schema does not require), which keeps only the id of the chunk it ranks.
-- Row-level security as in 0001.

CREATE TABLE IF NOT EXISTS knowledge_chunk (
  tenant_id uuid NOT NULL,
  chunk_id uuid NOT NULL,
  source_type text NOT NULL CHECK (source_type IN ('ARTICLE', 'DOC', 'POLICY')),
  source_ref text NOT NULL CHECK (char_length(source_ref) BETWEEN 1 AND 200),
  section text NOT NULL CHECK (char_length(section) BETWEEN 1 AND 200),
  body text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 20000),
  tsv tsvector GENERATED ALWAYS AS (to_tsvector('english', section || ' ' || body)) STORED,
  indexed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, chunk_id),
  UNIQUE (tenant_id, source_type, source_ref, section)
);
CREATE INDEX IF NOT EXISTS knowledge_chunk_tsv ON knowledge_chunk USING gin (tsv);
ALTER TABLE knowledge_chunk ENABLE ROW LEVEL SECURITY;
ALTER TABLE knowledge_chunk FORCE ROW LEVEL SECURITY;
CREATE POLICY knowledge_chunk_tenant_access ON knowledge_chunk AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON knowledge_chunk TO passage_app';
  END IF;
END
$$;
