-- Automatic clearing rules and stored runs (FR-11.20, AC-11.13).

-- A clearing document may now come from an automatic run.
ALTER TABLE clearing_document DROP CONSTRAINT IF EXISTS clearing_document_mode_check;
ALTER TABLE clearing_document ADD CONSTRAINT clearing_document_mode_check CHECK (mode IN ('manual', 'auto'));

-- Rules per ledger and account range (OB74 semantics): the ordered criteria group the open
-- items of the range; a group that nets to zero (or within the named tolerance group when
-- allow_tolerance is set) clears as one document. Maintained in place, never deleted.
CREATE TABLE IF NOT EXISTS auto_clearing_rule (
  tenant_id            uuid        NOT NULL,
  id                   uuid        NOT NULL,
  legal_entity_id      uuid        NOT NULL,
  ledger_id            uuid        NOT NULL,
  name                 text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
  account_from         text        NOT NULL CHECK (char_length(account_from) BETWEEN 1 AND 20),
  account_to           text        NOT NULL CHECK (char_length(account_to) BETWEEN 1 AND 20),
  criteria             text[]      NOT NULL CHECK (cardinality(criteria) BETWEEN 1 AND 5),
  allow_tolerance      boolean     NOT NULL DEFAULT false,
  tolerance_group_code text,
  sequence             int         NOT NULL DEFAULT 100 CHECK (sequence BETWEEN 1 AND 9999),
  active               boolean     NOT NULL DEFAULT true,
  version              int         NOT NULL DEFAULT 1,
  created_by           text        NOT NULL DEFAULT '',
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK (allow_tolerance = (tolerance_group_code IS NOT NULL)),
  FOREIGN KEY (tenant_id, ledger_id) REFERENCES ledger (tenant_id, id),
  FOREIGN KEY (tenant_id, ledger_id, tolerance_group_code) REFERENCES tolerance_group (tenant_id, ledger_id, code)
);
CREATE INDEX IF NOT EXISTS auto_clearing_rule_ledger_idx ON auto_clearing_rule (tenant_id, ledger_id, sequence, name);

-- One stored run per committed (clear mode) invocation. Simulations are not stored.
CREATE TABLE IF NOT EXISTS auto_clearing_run (
  tenant_id        uuid        NOT NULL,
  id               uuid        NOT NULL,
  legal_entity_id  uuid        NOT NULL,
  ledger_id        uuid        NOT NULL,
  rule_id          uuid,
  clearing_date    date        NOT NULL,
  groups_found     int         NOT NULL CHECK (groups_found >= 0),
  groups_cleared   int         NOT NULL CHECK (groups_cleared >= 0),
  groups_skipped   int         NOT NULL CHECK (groups_skipped >= 0),
  items_cleared    int         NOT NULL CHECK (items_cleared >= 0),
  unmatched_items  int         NOT NULL CHECK (unmatched_items >= 0),
  truncated        boolean     NOT NULL DEFAULT false,
  idempotency_key  text        NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
  payload_hash     bytea       NOT NULL CHECK (octet_length(payload_hash) = 32),
  created_by       text        NOT NULL DEFAULT '',
  created_at       timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, ledger_id, idempotency_key),
  FOREIGN KEY (tenant_id, ledger_id) REFERENCES ledger (tenant_id, id),
  FOREIGN KEY (tenant_id, rule_id) REFERENCES auto_clearing_rule (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS auto_clearing_run_ledger_idx ON auto_clearing_run (tenant_id, ledger_id, created_at DESC);

-- The run log: one row per group of two or more items, cleared or skipped with the reason.
CREATE TABLE IF NOT EXISTS auto_clearing_run_group (
  tenant_id            uuid          NOT NULL,
  legal_entity_id      uuid          NOT NULL,
  run_id               uuid          NOT NULL,
  group_no             int           NOT NULL CHECK (group_no >= 1),
  rule_id              uuid          NOT NULL,
  account              text          NOT NULL,
  group_hash           text          NOT NULL,
  item_count           int           NOT NULL CHECK (item_count >= 2),
  net                  numeric(20,6) NOT NULL,
  status               text          NOT NULL CHECK (status IN ('cleared', 'skipped')),
  reason_code          text,
  reason               text,
  clearing_document_id uuid,
  PRIMARY KEY (tenant_id, run_id, group_no),
  FOREIGN KEY (tenant_id, run_id) REFERENCES auto_clearing_run (tenant_id, id),
  FOREIGN KEY (tenant_id, rule_id) REFERENCES auto_clearing_rule (tenant_id, id),
  FOREIGN KEY (tenant_id, clearing_document_id) REFERENCES clearing_document (tenant_id, id)
);

ALTER TABLE auto_clearing_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE auto_clearing_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY auto_clearing_rule_tenant_access ON auto_clearing_rule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY auto_clearing_rule_legal_entity_scope ON auto_clearing_rule AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE auto_clearing_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE auto_clearing_run FORCE ROW LEVEL SECURITY;
CREATE POLICY auto_clearing_run_tenant_access ON auto_clearing_run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY auto_clearing_run_legal_entity_scope ON auto_clearing_run AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE auto_clearing_run_group ENABLE ROW LEVEL SECURITY;
ALTER TABLE auto_clearing_run_group FORCE ROW LEVEL SECURITY;
CREATE POLICY auto_clearing_run_group_tenant_access ON auto_clearing_run_group AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY auto_clearing_run_group_legal_entity_scope ON auto_clearing_run_group AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Rules are updated in place; runs and their group log are written once.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON auto_clearing_rule TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON auto_clearing_run, auto_clearing_run_group TO passage_app';
  END IF;
END
$$;
