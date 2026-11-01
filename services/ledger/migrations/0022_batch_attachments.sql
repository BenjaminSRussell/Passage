-- Voucher attachments and notes on journal batches (FR-11.11, FR-11.18). A row is either a file
-- reference (kind 'attachment': store reference, document type, retention class, content type,
-- size) or a note. The ledger holds metadata only; file bytes live in the document store. Rows
-- are inserted while the batch is editable and become immutable when it is posted: the service
-- refuses a later insert, and refuses a delete until the retention class has run from the
-- posting date (LEDGER_ATTACHMENT_RETAINED). Retained-until is derived from the batch's
-- posted_at, so no row is ever updated.
CREATE TABLE IF NOT EXISTS batch_attachment (
  tenant_id        uuid        NOT NULL,
  id               uuid        NOT NULL,
  batch_id         uuid        NOT NULL,
  legal_entity_id  uuid        NOT NULL,
  kind             text        NOT NULL CHECK (kind IN ('attachment', 'note')),
  name             text        NOT NULL DEFAULT '' CHECK (char_length(name) <= 200),
  document_type    text        NOT NULL DEFAULT '' CHECK (document_type IN ('', 'invoice_image', 'approval_evidence', 'supporting_calculation', 'correspondence', 'other')),
  retention_class  text        NOT NULL DEFAULT '' CHECK (retention_class IN ('', 'financial_record_10y', 'financial_record_7y', 'financial_record_6y', 'working_paper_2y')),
  store_ref        text        NOT NULL DEFAULT '' CHECK (char_length(store_ref) <= 1024),
  content_type     text        NOT NULL DEFAULT '' CHECK (char_length(content_type) <= 100),
  size_bytes       bigint      NOT NULL DEFAULT 0 CHECK (size_bytes BETWEEN 0 AND 26214400),
  note             text        NOT NULL DEFAULT '' CHECK (char_length(note) <= 2000),
  created_by       text        NOT NULL DEFAULT '',
  created_at       timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, batch_id) REFERENCES journal_batch (tenant_id, id),
  CHECK ((kind = 'note' AND note <> '' AND store_ref = '') OR (kind = 'attachment' AND name <> '' AND store_ref <> '' AND size_bytes > 0))
);
CREATE INDEX IF NOT EXISTS batch_attachment_batch ON batch_attachment (tenant_id, batch_id, created_at, id);

ALTER TABLE batch_attachment ENABLE ROW LEVEL SECURITY;
ALTER TABLE batch_attachment FORCE ROW LEVEL SECURITY;
CREATE POLICY batch_attachment_tenant_access ON batch_attachment AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY batch_attachment_legal_entity_scope ON batch_attachment AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Inserted and deleted (before posting, or after retention); never updated.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON batch_attachment TO passage_app';
  END IF;
END
$$;
