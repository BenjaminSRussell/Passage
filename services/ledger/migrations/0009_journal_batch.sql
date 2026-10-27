-- Manual journal workspace (FR-11.9, BR-11.10). A journal name (AX
-- LedgerJournalName) carries the controls of its batches: approval workflow,
-- self-approval, the reversal reason rule (FR-11.15), the reconciliation
-- account override (BR-11.8) and the lines limit. A batch is an unposted set of
-- lines; posting it creates one journal entry through the posting pipeline in
-- the same transaction and records the entry id. Batch lines change only while
-- the batch is editable; posted entries stay immutable in journal_entry.
-- Additive only.

CREATE TABLE IF NOT EXISTS journal_name (
    tenant_id                uuid        NOT NULL,
    code                     text        NOT NULL CHECK (code ~ '^[A-Za-z0-9_-]{1,20}$'),
    name                     text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60),
    requires_approval        boolean     NOT NULL DEFAULT false,
    self_approval_allowed    boolean     NOT NULL DEFAULT false,
    reversal_reason_required boolean     NOT NULL DEFAULT false,
    allow_recon_accounts     boolean     NOT NULL DEFAULT false,
    lines_limit              int         NOT NULL DEFAULT 5000 CHECK (lines_limit BETWEEN 1 AND 10000),
    active                   boolean     NOT NULL DEFAULT true,
    created_at               timestamptz NOT NULL DEFAULT now(),
    updated_at               timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, code)
);

CREATE TABLE IF NOT EXISTS journal_batch (
    tenant_id       uuid        NOT NULL,
    id              uuid        NOT NULL,
    ledger_id       uuid        NOT NULL,
    legal_entity_id uuid        NOT NULL,
    journal_name    text        NOT NULL,
    description     text        NOT NULL DEFAULT '' CHECK (char_length(description) <= 255),
    posting_date    date        NOT NULL,
    document_date   date,
    status          text        NOT NULL CHECK (status IN ('draft', 'validated', 'in_approval', 'approved',
                                                          'rejected', 'posting', 'posted', 'cancelled')),
    version         int         NOT NULL DEFAULT 1 CHECK (version >= 1),
    submitted_by    text,
    decided_by      text,
    posted_by       text,
    posted_at       timestamptz,
    post_key        text        CHECK (post_key IS NULL OR char_length(post_key) BETWEEN 1 AND 128),
    posted_entry_id uuid,
    last_error      text        CHECK (last_error IS NULL OR char_length(last_error) <= 1000),
    created_by      text        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, ledger_id) REFERENCES ledger (tenant_id, id),
    FOREIGN KEY (tenant_id, journal_name) REFERENCES journal_name (tenant_id, code),
    FOREIGN KEY (tenant_id, posted_entry_id) REFERENCES journal_entry (tenant_id, id),
    CHECK ((status = 'posted') = (posted_entry_id IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS journal_batch_ledger_status_ix
    ON journal_batch (tenant_id, ledger_id, status, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS journal_batch_posted_entry_uq
    ON journal_batch (tenant_id, posted_entry_id) WHERE posted_entry_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS journal_batch_line (
    tenant_id       uuid          NOT NULL,
    batch_id        uuid          NOT NULL,
    line_number     int           NOT NULL CHECK (line_number BETWEEN 1 AND 10000),
    legal_entity_id uuid          NOT NULL,
    account         text          NOT NULL CHECK (char_length(account) BETWEEN 1 AND 20),
    dimensions      jsonb         NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(dimensions) = 'object'),
    debit_credit    char(1)       NOT NULL CHECK (debit_credit IN ('D', 'C')),
    amount          numeric(20,6) NOT NULL CHECK (amount >= 0),
    currency        char(3)       NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    line_text       text          CHECK (line_text IS NULL OR char_length(line_text) <= 255),
    source_line_ref text          CHECK (source_line_ref IS NULL OR char_length(source_line_ref) <= 200),
    PRIMARY KEY (tenant_id, batch_id, line_number),
    FOREIGN KEY (tenant_id, batch_id) REFERENCES journal_batch (tenant_id, id) ON DELETE CASCADE
);

ALTER TABLE journal_name ENABLE ROW LEVEL SECURITY;
ALTER TABLE journal_name FORCE ROW LEVEL SECURITY;
CREATE POLICY journal_name_tenant_access ON journal_name AS PERMISSIVE
    USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE journal_batch ENABLE ROW LEVEL SECURITY;
ALTER TABLE journal_batch FORCE ROW LEVEL SECURITY;
CREATE POLICY journal_batch_tenant_access ON journal_batch AS PERMISSIVE
    USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY journal_batch_legal_entity_scope ON journal_batch AS RESTRICTIVE
    USING (current_setting('passage.legal_entity_scopes', true) = '*'
           OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE journal_batch_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE journal_batch_line FORCE ROW LEVEL SECURITY;
CREATE POLICY journal_batch_line_tenant_access ON journal_batch_line AS PERMISSIVE
    USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY journal_batch_line_legal_entity_scope ON journal_batch_line AS RESTRICTIVE
    USING (current_setting('passage.legal_entity_scopes', true) = '*'
           OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The app role needs the new tables (0005_grants.sql cannot cover them).
-- Journal names and batches are never deleted (batches are cancelled); batch
-- lines are replaced wholesale on edit, so they need DELETE.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON journal_name, journal_batch TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON journal_batch_line TO passage_app';
  END IF;
END
$$;
