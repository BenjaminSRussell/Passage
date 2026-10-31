-- Open-item managed G/L accounts and manual clearing (FR-11.19, BR-11.12, BR-11.13, BR-11.18).

-- Per ledger and account: is the account open-item managed? Rows are written once and
-- switched on and off (UPDATE); they are never deleted.
CREATE TABLE IF NOT EXISTS open_item_setting (
  tenant_id       uuid        NOT NULL,
  legal_entity_id uuid        NOT NULL,
  ledger_id       uuid        NOT NULL,
  main_account_id uuid        NOT NULL,
  enabled         boolean     NOT NULL,
  changed_by      text        NOT NULL DEFAULT '',
  changed_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, ledger_id, main_account_id),
  FOREIGN KEY (tenant_id, ledger_id) REFERENCES ledger (tenant_id, id),
  FOREIGN KEY (tenant_id, main_account_id) REFERENCES main_account (tenant_id, id)
);

-- Tolerance groups (OBA0 semantics): the largest residual a clearing may write off to a
-- difference account, per side (BR-11.12: debit_limit for a debit residual, credit_limit
-- for a credit residual) and, when percent_limit is above zero, at most that share of
-- the selection's absolute total. One per ledger and code; maintained in place, never deleted.
CREATE TABLE IF NOT EXISTS tolerance_group (
  tenant_id             uuid          NOT NULL,
  legal_entity_id       uuid          NOT NULL,
  ledger_id             uuid          NOT NULL,
  code                  text          NOT NULL CHECK (code ~ '^[A-Z0-9_]{1,10}$'),
  name                  text          NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60),
  debit_limit           numeric(20,6) NOT NULL CHECK (debit_limit >= 0),
  credit_limit          numeric(20,6) NOT NULL CHECK (credit_limit >= 0),
  percent_limit         numeric(9,6)  NOT NULL DEFAULT 0 CHECK (percent_limit BETWEEN 0 AND 100),
  difference_account_id uuid          NOT NULL,
  active                boolean       NOT NULL DEFAULT true,
  version               int           NOT NULL DEFAULT 1,
  created_at            timestamptz   NOT NULL DEFAULT now(),
  updated_at            timestamptz   NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, ledger_id, code),
  FOREIGN KEY (tenant_id, ledger_id) REFERENCES ledger (tenant_id, id),
  FOREIGN KEY (tenant_id, difference_account_id) REFERENCES main_account (tenant_id, id)
);

-- A clearing document groups the open items it cleared (BR-11.13: reset as a whole).
CREATE TABLE IF NOT EXISTS clearing_document (
  tenant_id            uuid          NOT NULL,
  id                   uuid          NOT NULL,
  legal_entity_id      uuid          NOT NULL,
  ledger_id            uuid          NOT NULL,
  main_account_id      uuid          NOT NULL,
  clearing_date        date          NOT NULL,
  mode                 text          NOT NULL CHECK (mode IN ('manual')),
  item_ids             uuid[]        NOT NULL CHECK (cardinality(item_ids) BETWEEN 2 AND 10000),
  tolerance_group_code text,
  difference_amount    numeric(20,6) NOT NULL DEFAULT 0,
  difference_entry_id  uuid,
  residual_item_id     uuid,
  status               text          NOT NULL CHECK (status IN ('cleared', 'reset')),
  idempotency_key      text          NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
  payload_hash         bytea         NOT NULL CHECK (octet_length(payload_hash) = 32),
  reset_reason_code    text,
  reset_comment        text,
  reset_key            text,
  reset_by             text,
  reset_at             timestamptz,
  created_by           text          NOT NULL DEFAULT '',
  created_at           timestamptz   NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, ledger_id, idempotency_key),
  FOREIGN KEY (tenant_id, ledger_id) REFERENCES ledger (tenant_id, id),
  FOREIGN KEY (tenant_id, main_account_id) REFERENCES main_account (tenant_id, id),
  FOREIGN KEY (tenant_id, difference_entry_id) REFERENCES journal_entry (tenant_id, id)
);

-- One open item per line posted on an open-item managed account, signed (debit positive)
-- in the ledger currency. Residual items carry no journal line. status moves
-- open -> cleared -> reset_open (clearable again); a residual item of a reset clearing is voided.
CREATE TABLE IF NOT EXISTS open_item (
  tenant_id                uuid          NOT NULL,
  id                       uuid          NOT NULL,
  seq                      bigint        GENERATED ALWAYS AS IDENTITY,
  legal_entity_id          uuid          NOT NULL,
  ledger_id                uuid          NOT NULL,
  main_account_id          uuid          NOT NULL,
  dimension_combination_id bytea         NOT NULL,
  journal_line_id          uuid,
  source_type              text          NOT NULL CHECK (source_type IN ('gl_line', 'residual')),
  amount                   numeric(20,6) NOT NULL CHECK (amount <> 0),
  currency                 char(3)       NOT NULL,
  assignment               text          CHECK (char_length(assignment) <= 50),
  posting_date             date          NOT NULL,
  status                   text          NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'cleared', 'reset_open', 'voided')),
  clearing_document_id     uuid,
  residual_of_item_id      uuid,
  created_at               timestamptz   NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, journal_line_id),
  FOREIGN KEY (tenant_id, ledger_id) REFERENCES ledger (tenant_id, id),
  FOREIGN KEY (tenant_id, main_account_id) REFERENCES main_account (tenant_id, id),
  FOREIGN KEY (tenant_id, dimension_combination_id) REFERENCES dimension_combination (tenant_id, id),
  FOREIGN KEY (tenant_id, journal_line_id) REFERENCES journal_line (tenant_id, id),
  FOREIGN KEY (tenant_id, clearing_document_id) REFERENCES clearing_document (tenant_id, id),
  FOREIGN KEY (tenant_id, residual_of_item_id) REFERENCES open_item (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS open_item_seq_idx ON open_item (seq);
CREATE INDEX IF NOT EXISTS open_item_lookup_idx ON open_item (tenant_id, ledger_id, main_account_id, status, assignment);
CREATE INDEX IF NOT EXISTS clearing_document_ledger_idx ON clearing_document (tenant_id, ledger_id, created_at DESC);

ALTER TABLE open_item_setting ENABLE ROW LEVEL SECURITY;
ALTER TABLE open_item_setting FORCE ROW LEVEL SECURITY;
CREATE POLICY open_item_setting_tenant_access ON open_item_setting AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY open_item_setting_legal_entity_scope ON open_item_setting AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE tolerance_group ENABLE ROW LEVEL SECURITY;
ALTER TABLE tolerance_group FORCE ROW LEVEL SECURITY;
CREATE POLICY tolerance_group_tenant_access ON tolerance_group AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY tolerance_group_legal_entity_scope ON tolerance_group AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE clearing_document ENABLE ROW LEVEL SECURITY;
ALTER TABLE clearing_document FORCE ROW LEVEL SECURITY;
CREATE POLICY clearing_document_tenant_access ON clearing_document AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY clearing_document_legal_entity_scope ON clearing_document AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE open_item ENABLE ROW LEVEL SECURITY;
ALTER TABLE open_item FORCE ROW LEVEL SECURITY;
CREATE POLICY open_item_tenant_access ON open_item AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY open_item_legal_entity_scope ON open_item AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Every table is updated in place (settings switch, groups are maintained, clearing marks
-- items and resets documents); none is deleted.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON open_item_setting, tolerance_group, clearing_document, open_item TO passage_app';
  END IF;
END
$$;
