-- Break-glass review of PHI reads (#606, HIPAA emergency access). A read whose purpose starts with the
-- reserved reason code BREAK-GLASS is emergency access: the reader used a duty kept for emergencies. A
-- second person reviews each such read once (APPROVED, or FLAGGED with a note); the reviewer is never
-- the reader. phi_read stays untouched and hash-chained; the review is a separate append-only row.
CREATE TABLE IF NOT EXISTS phi_read_review (
  tenant_id uuid NOT NULL,
  read_id uuid NOT NULL,
  outcome text NOT NULL CHECK (outcome IN ('APPROVED', 'FLAGGED')),
  note text NOT NULL DEFAULT '' CHECK (length(note) <= 1000),
  reviewer_id text NOT NULL CHECK (length(reviewer_id) BETWEEN 1 AND 256),
  reviewed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, read_id),
  FOREIGN KEY (tenant_id, read_id) REFERENCES phi_read (tenant_id, id),
  CHECK (outcome = 'APPROVED' OR length(btrim(note)) > 0)
);
ALTER TABLE phi_read_review ENABLE ROW LEVEL SECURITY;
ALTER TABLE phi_read_review FORCE ROW LEVEL SECURITY;
CREATE POLICY phi_read_review_tenant_access ON phi_read_review AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Break-glass reads, oldest first, for the review queue.
CREATE INDEX IF NOT EXISTS phi_read_break_glass ON phi_read (tenant_id, seq)
  WHERE upper(purpose) LIKE 'BREAK-GLASS%';

CREATE FUNCTION phi_read_review_immutable() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'phi_read_review is append-only: a read is reviewed once';
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER phi_read_review_no_mutation
  BEFORE UPDATE OR DELETE ON phi_read_review
  FOR EACH ROW EXECUTE PROCEDURE phi_read_review_immutable();
CREATE TRIGGER phi_read_review_no_truncate
  BEFORE TRUNCATE ON phi_read_review
  FOR EACH STATEMENT EXECUTE PROCEDURE phi_read_review_immutable();

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON phi_read_review TO passage_app';
  END IF;
END
$$;
