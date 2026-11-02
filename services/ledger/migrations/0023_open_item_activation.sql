-- Activation run for an open-item managed account with history (FR-11.19, BR-11.18):
-- one opening open item per non-zero (dimension combination, transaction currency) balance of the
-- lines dated on or before the activation date, and a gl_line item per line dated after it.

CREATE TABLE IF NOT EXISTS open_item_activation_run (
  tenant_id       uuid          NOT NULL,
  id              uuid          NOT NULL,
  legal_entity_id uuid          NOT NULL,
  ledger_id       uuid          NOT NULL,
  main_account_id uuid          NOT NULL,
  activation_date date          NOT NULL,
  items_created   int           NOT NULL CHECK (items_created >= 0),
  line_items_created int        NOT NULL DEFAULT 0 CHECK (line_items_created >= 0),
  total_debit     numeric(20,6) NOT NULL CHECK (total_debit >= 0),
  total_credit    numeric(20,6) NOT NULL CHECK (total_credit >= 0),
  idempotency_key text          NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
  payload_hash    bytea         NOT NULL CHECK (octet_length(payload_hash) = 32),
  created_by      text          NOT NULL DEFAULT '',
  created_at      timestamptz   NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, ledger_id, idempotency_key),
  FOREIGN KEY (tenant_id, ledger_id) REFERENCES ledger (tenant_id, id),
  FOREIGN KEY (tenant_id, main_account_id) REFERENCES main_account (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS open_item_activation_run_ledger_idx ON open_item_activation_run (tenant_id, ledger_id, created_at DESC);

ALTER TABLE open_item_setting ADD COLUMN IF NOT EXISTS activation_run_id uuid;
ALTER TABLE open_item ADD COLUMN IF NOT EXISTS activation_run_id uuid;

ALTER TABLE open_item DROP CONSTRAINT IF EXISTS open_item_source_type_check;
ALTER TABLE open_item ADD CONSTRAINT open_item_source_type_check CHECK (source_type IN ('gl_line', 'residual', 'activation'));

ALTER TABLE open_item_activation_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE open_item_activation_run FORCE ROW LEVEL SECURITY;
CREATE POLICY open_item_activation_run_tenant_access ON open_item_activation_run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY open_item_activation_run_legal_entity_scope ON open_item_activation_run AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Runs are written once; the setting and the items already hold UPDATE from 0014.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON open_item_activation_run TO passage_app';
  END IF;
END
$$;
