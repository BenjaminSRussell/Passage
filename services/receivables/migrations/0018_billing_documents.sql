-- #30: billing's posted invoices and credit notes become open items. source_doc_id is the billing document and installment_no the
-- installment it opened (a credit note opens installment 1), so a replayed or duplicate event finds the items it already made
-- and creates nothing. original_doc_id is the invoice a credit note credits. Items entered by hand, imported or opened for an
-- RMA credit carry no source document; they are protected by the unique document number of the legal entity instead, which
-- the consumer shares with them: billing's number is the document number.
ALTER TABLE open_item ADD COLUMN IF NOT EXISTS source_doc_id uuid;
ALTER TABLE open_item ADD COLUMN IF NOT EXISTS installment_no integer CHECK (installment_no IS NULL OR installment_no >= 1);
ALTER TABLE open_item ADD COLUMN IF NOT EXISTS original_doc_id uuid;
ALTER TABLE open_item DROP CONSTRAINT IF EXISTS open_item_source_columns;
ALTER TABLE open_item ADD CONSTRAINT open_item_source_columns CHECK (
  (source_doc_id IS NULL) = (installment_no IS NULL) AND (original_doc_id IS NULL OR source_doc_id IS NOT NULL));
CREATE UNIQUE INDEX IF NOT EXISTS open_item_source_key ON open_item (tenant_id, source_type, source_doc_id, installment_no)
  WHERE source_doc_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS open_item_original_doc ON open_item (tenant_id, original_doc_id) WHERE original_doc_id IS NOT NULL;

-- The platform eventing inbox (eventing.NewPgInbox): one row per consumer and event id, written once the event is applied.
CREATE TABLE IF NOT EXISTS consumer_inbox (
  tenant_id uuid NOT NULL,
  consumer text NOT NULL,
  event_id text NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, consumer, event_id)
);
ALTER TABLE consumer_inbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE consumer_inbox FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS consumer_inbox_tenant_access ON consumer_inbox;
CREATE POLICY consumer_inbox_tenant_access ON consumer_inbox AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON consumer_inbox TO passage_app';
  END IF;
END
$$;
