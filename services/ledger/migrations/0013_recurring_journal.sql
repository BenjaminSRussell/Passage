-- Recurring journals (FR-11.10, AX periodic journals, SAP FBD1). A recurring
-- journal is a template of journal lines (fixed amounts, or percentages of a
-- base amount supplied at generation) plus a calendar schedule. Generating it
-- for a scheduled date creates one draft journal batch; the run table makes the
-- generation idempotent per (template, scheduled date). Generated batches follow
-- the normal batch lifecycle (validation, approval, posting). Additive only.

CREATE TABLE IF NOT EXISTS recurring_journal (
    tenant_id       uuid          NOT NULL,
    id              uuid          NOT NULL,
    ledger_id       uuid          NOT NULL,
    legal_entity_id uuid          NOT NULL,
    journal_name    text          NOT NULL,
    name            text          NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
    description     text          NOT NULL DEFAULT '' CHECK (char_length(description) <= 255),
    frequency       text          NOT NULL CHECK (frequency IN ('monthly', 'quarterly', 'yearly')),
    day_of_month    int           NOT NULL CHECK (day_of_month BETWEEN 0 AND 31),
    start_date      date          NOT NULL,
    end_date        date,
    base_amount     numeric(20,6) CHECK (base_amount IS NULL OR base_amount >= 0),
    status          text          NOT NULL CHECK (status IN ('active', 'paused')),
    next_run_date   date,
    last_run_date   date,
    version         int           NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_by      text          NOT NULL,
    created_at      timestamptz   NOT NULL DEFAULT now(),
    updated_at      timestamptz   NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, ledger_id) REFERENCES ledger (tenant_id, id),
    FOREIGN KEY (tenant_id, journal_name) REFERENCES journal_name (tenant_id, code),
    CHECK (end_date IS NULL OR end_date >= start_date)
);

CREATE INDEX IF NOT EXISTS recurring_journal_due_ix
    ON recurring_journal (tenant_id, status, next_run_date);

CREATE TABLE IF NOT EXISTS recurring_journal_line (
    tenant_id       uuid          NOT NULL,
    recurring_id    uuid          NOT NULL,
    line_number     int           NOT NULL CHECK (line_number BETWEEN 1 AND 10000),
    legal_entity_id uuid          NOT NULL,
    account         text          NOT NULL CHECK (char_length(account) BETWEEN 1 AND 20),
    dimensions      jsonb         NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(dimensions) = 'object'),
    debit_credit    char(1)       NOT NULL CHECK (debit_credit IN ('D', 'C')),
    amount_kind     text          NOT NULL CHECK (amount_kind IN ('fixed', 'percent')),
    amount          numeric(20,6) NOT NULL CHECK (amount >= 0),
    currency        char(3)       NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    line_text       text          CHECK (line_text IS NULL OR char_length(line_text) <= 255),
    PRIMARY KEY (tenant_id, recurring_id, line_number),
    FOREIGN KEY (tenant_id, recurring_id) REFERENCES recurring_journal (tenant_id, id) ON DELETE CASCADE,
    CHECK (amount_kind = 'fixed' OR amount <= 1000)
);

CREATE TABLE IF NOT EXISTS recurring_journal_run (
    tenant_id       uuid        NOT NULL,
    id              uuid        NOT NULL,
    recurring_id    uuid        NOT NULL,
    legal_entity_id uuid        NOT NULL,
    scheduled_date  date        NOT NULL,
    batch_id        uuid        NOT NULL,
    generated_by    text        NOT NULL,
    generated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, recurring_id) REFERENCES recurring_journal (tenant_id, id),
    FOREIGN KEY (tenant_id, batch_id) REFERENCES journal_batch (tenant_id, id),
    UNIQUE (tenant_id, recurring_id, scheduled_date)
);

ALTER TABLE recurring_journal ENABLE ROW LEVEL SECURITY;
ALTER TABLE recurring_journal FORCE ROW LEVEL SECURITY;
CREATE POLICY recurring_journal_tenant_access ON recurring_journal AS PERMISSIVE
    USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY recurring_journal_legal_entity_scope ON recurring_journal AS RESTRICTIVE
    USING (current_setting('passage.legal_entity_scopes', true) = '*'
           OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE recurring_journal_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE recurring_journal_line FORCE ROW LEVEL SECURITY;
CREATE POLICY recurring_journal_line_tenant_access ON recurring_journal_line AS PERMISSIVE
    USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY recurring_journal_line_legal_entity_scope ON recurring_journal_line AS RESTRICTIVE
    USING (current_setting('passage.legal_entity_scopes', true) = '*'
           OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE recurring_journal_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE recurring_journal_run FORCE ROW LEVEL SECURITY;
CREATE POLICY recurring_journal_run_tenant_access ON recurring_journal_run AS PERMISSIVE
    USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY recurring_journal_run_legal_entity_scope ON recurring_journal_run AS RESTRICTIVE
    USING (current_setting('passage.legal_entity_scopes', true) = '*'
           OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Templates are updated in place and their lines replaced wholesale on a change
-- (DELETE on lines); runs are an append-only history (SELECT, INSERT).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON recurring_journal TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON recurring_journal_line TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON recurring_journal_run TO passage_app';
  END IF;
END
$$;
