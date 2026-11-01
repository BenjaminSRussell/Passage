-- Dimension realignment (FR-11.24, AC-11.17): a controlled restatement that moves posted balances from
-- one dimension combination to another through ordinary reclassification entries, one per period.
-- A run is proposed (simulated), then posted, then possibly reversed as a unit, or cancelled.
CREATE TABLE IF NOT EXISTS realignment_run (
  tenant_id            uuid        NOT NULL,
  id                   uuid        NOT NULL,
  legal_entity_id      uuid        NOT NULL,
  ledger_id            uuid        NOT NULL,
  filter               jsonb       NOT NULL CHECK (jsonb_typeof(filter) = 'object'),
  mapping              jsonb       NOT NULL CHECK (jsonb_typeof(mapping) = 'object'),
  status               text        NOT NULL DEFAULT 'proposed' CHECK (status IN ('proposed', 'posted', 'reversed', 'cancelled')),
  reason_code          text,
  reason_comment       text,
  combinations_affected int        NOT NULL DEFAULT 0,
  line_count           bigint      NOT NULL DEFAULT 0,
  amount_moved         numeric(20,6) NOT NULL DEFAULT 0,
  preview              jsonb       NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(preview) = 'array'),
  entry_ids            uuid[]      NOT NULL DEFAULT '{}',
  reversal_entry_ids   uuid[]      NOT NULL DEFAULT '{}',
  idempotency_key      text        NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
  payload_hash         bytea       NOT NULL CHECK (octet_length(payload_hash) = 32),
  version              int         NOT NULL DEFAULT 1,
  created_by           text        NOT NULL DEFAULT '',
  created_at           timestamptz NOT NULL DEFAULT now(),
  posted_by            text,
  posted_at            timestamptz,
  reversed_by          text,
  reversed_at          timestamptz,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, ledger_id) REFERENCES ledger (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS realignment_run_ledger_idx ON realignment_run (tenant_id, ledger_id, created_at DESC);

ALTER TABLE realignment_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE realignment_run FORCE ROW LEVEL SECURITY;
CREATE POLICY realignment_run_tenant_access ON realignment_run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY realignment_run_legal_entity_scope ON realignment_run AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Reclassification entries (and their reversals) carry the run that made them (FR-11.24), so the
-- audit trail can list them as kind realignment_run. Adding columns does not touch the append-only triggers.
ALTER TABLE journal_entry ADD COLUMN IF NOT EXISTS is_realignment boolean NOT NULL DEFAULT false;
ALTER TABLE journal_entry ADD COLUMN IF NOT EXISTS realignment_run_id uuid;

-- Runs change status in place and are never deleted.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON realignment_run TO passage_app';
  END IF;
END
$$;
