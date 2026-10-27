-- Wave 16 slice 4: journal reversal (BR-16.12) and tag counting (FR-16.18).
-- Additive only: two nullable link columns and two tables.

-- A reversal journal points at the POSTED journal it negates; one reversal per
-- original. Each reversal line points at the line it negates, and its posted
-- transaction carries corrects_transaction_id (FR-16.1).
ALTER TABLE inventory_journal ADD COLUMN IF NOT EXISTS reverses_journal_id uuid;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'inventory_journal_reverses_fk') THEN
    ALTER TABLE inventory_journal ADD CONSTRAINT inventory_journal_reverses_fk
      FOREIGN KEY (tenant_id, reverses_journal_id) REFERENCES inventory_journal (tenant_id, id);
  END IF;
END
$$;
CREATE UNIQUE INDEX IF NOT EXISTS inventory_journal_reverses_uk
  ON inventory_journal (tenant_id, reverses_journal_id) WHERE reverses_journal_id IS NOT NULL;

ALTER TABLE inventory_journal_line ADD COLUMN IF NOT EXISTS reverses_line_id uuid;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'inventory_journal_line_reverses_fk') THEN
    ALTER TABLE inventory_journal_line ADD CONSTRAINT inventory_journal_line_reverses_fk
      FOREIGN KEY (tenant_id, reverses_line_id) REFERENCES inventory_journal_line (tenant_id, id);
  END IF;
END
$$;
CREATE INDEX IF NOT EXISTS inventory_transaction_corrects_idx
  ON inventory_transaction (tenant_id, corrects_transaction_id) WHERE corrects_transaction_id IS NOT NULL;

-- One range per :generate-tags call of a plan; the key makes the call replayable.
CREATE TABLE IF NOT EXISTS count_tag_range (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  id uuid NOT NULL,
  plan_id uuid NOT NULL,
  idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 255),
  first_seq bigint NOT NULL CHECK (first_seq > 0),
  last_seq bigint NOT NULL CHECK (last_seq >= first_seq),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, plan_id) REFERENCES count_plan (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS count_tag_range_seq_idx ON count_tag_range (tenant_id, legal_entity_id, last_seq);
ALTER TABLE count_tag_range ENABLE ROW LEVEL SECURITY;
ALTER TABLE count_tag_range FORCE ROW LEVEL SECURITY;
CREATE POLICY count_tag_range_tenant_access ON count_tag_range AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY count_tag_range_legal_entity_scope ON count_tag_range AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- One printed tag. seq is the legal entity's TAG- number, so a tag number is
-- consumed once (BR-16.15). dims is the normalized dimension set of the count.
-- sheet_id is set once the tag's quantity has moved to a count sheet; the tag
-- is then final.
CREATE TABLE IF NOT EXISTS count_tag (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  id uuid NOT NULL,
  range_id uuid NOT NULL,
  plan_id uuid NOT NULL,
  seq bigint NOT NULL CHECK (seq > 0),
  tag_number text NOT NULL,
  status text NOT NULL CHECK (status IN ('OPEN', 'USED', 'VOID', 'MISSING')),
  product_id uuid,
  dims jsonb NOT NULL DEFAULT '{}'::jsonb,
  qty numeric(28,6) NOT NULL DEFAULT 0 CHECK (qty >= 0),
  counted_by text,
  counted_at timestamptz,
  sheet_id uuid,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, seq),
  UNIQUE (tenant_id, legal_entity_id, tag_number),
  FOREIGN KEY (tenant_id, range_id) REFERENCES count_tag_range (tenant_id, id),
  FOREIGN KEY (tenant_id, plan_id) REFERENCES count_plan (tenant_id, id),
  FOREIGN KEY (tenant_id, sheet_id) REFERENCES count_sheet (tenant_id, id),
  CHECK (status <> 'USED' OR product_id IS NOT NULL),
  CHECK (sheet_id IS NULL OR status = 'USED')
);
CREATE INDEX IF NOT EXISTS count_tag_plan_idx ON count_tag (tenant_id, plan_id, seq);
ALTER TABLE count_tag ENABLE ROW LEVEL SECURITY;
ALTER TABLE count_tag FORCE ROW LEVEL SECURITY;
CREATE POLICY count_tag_tenant_access ON count_tag AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY count_tag_legal_entity_scope ON count_tag AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON count_tag_range, count_tag TO passage_app';
  END IF;
END
$$;
