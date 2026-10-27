-- Wave 33 slice 5: stability study lifecycle (FR-33.16), source-inspection receipt
-- checks (FR-33.19, BR-33.16) and certificates of analysis (FR-33.9, BR-33.10).

-- The last status change of a study: who, when and why (TERMINATED needs a reason).
ALTER TABLE quality_stability_study ADD COLUMN IF NOT EXISTS status_reason text NOT NULL DEFAULT ''
  CHECK (char_length(status_reason) <= 500);
ALTER TABLE quality_stability_study ADD COLUMN IF NOT EXISTS status_changed_by text NOT NULL DEFAULT '';
ALTER TABLE quality_stability_study ADD COLUMN IF NOT EXISTS status_changed_at timestamptz;
ALTER TABLE quality_stability_study DROP CONSTRAINT IF EXISTS quality_stability_study_terminate_reason;
ALTER TABLE quality_stability_study ADD CONSTRAINT quality_stability_study_terminate_reason
  CHECK (status <> 'TERMINATED' OR status_reason <> '');

-- Terminating a study cancels the pulls still PLANNED.
ALTER TABLE quality_pull_point DROP CONSTRAINT IF EXISTS quality_pull_point_status_check;
ALTER TABLE quality_pull_point ADD CONSTRAINT quality_pull_point_status_check
  CHECK (status IN ('PLANNED','ORDERED','PULLED','MISSED','CANCELLED'));

-- Accepted source inspections are found by PO line and batch.
CREATE INDEX IF NOT EXISTS quality_order_source_inspection ON quality_order (tenant_id, (source_ref->>'id'), (dims->>'batch'))
  WHERE trigger = 'SOURCE_INSPECTION';

-- One row per receipt line checked against source inspection (BR-33.16). The
-- covered quantity is used up: a later receipt of the same PO line and batch only
-- sees what earlier receipts left.
CREATE TABLE IF NOT EXISTS quality_receipt_check (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  po_line_id uuid NOT NULL,
  batch text NOT NULL CHECK (char_length(batch) BETWEEN 1 AND 60),
  receipt_line_id uuid NOT NULL,
  received_qty numeric(18,6) NOT NULL CHECK (received_qty > 0),
  accepted_qty numeric(18,6) NOT NULL CHECK (accepted_qty >= 0),
  covered_before numeric(18,6) NOT NULL CHECK (covered_before >= 0),
  covered_qty numeric(18,6) NOT NULL CHECK (covered_qty >= 0),
  normal_qty numeric(18,6) NOT NULL CHECK (normal_qty >= 0),
  skip boolean NOT NULL,
  checked_by text NOT NULL,
  checked_at timestamptz NOT NULL,
  idempotency_key text,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, receipt_line_id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (covered_qty + normal_qty = received_qty),
  CHECK (skip = (normal_qty = 0))
);
CREATE INDEX IF NOT EXISTS quality_receipt_check_line ON quality_receipt_check (tenant_id, po_line_id, batch, checked_at);
ALTER TABLE quality_receipt_check ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality_receipt_check FORCE ROW LEVEL SECURITY;
CREATE POLICY quality_receipt_check_tenant_access ON quality_receipt_check AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Certificates of analysis: immutable revisions per (order, shipment); shipment_key
-- is '' for the batch CoA. The document is stored as issued.
CREATE TABLE IF NOT EXISTS quality_coa (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  quality_order_id uuid NOT NULL,
  shipment_key text NOT NULL DEFAULT '',
  revision int NOT NULL CHECK (revision >= 1),
  supersedes uuid,
  reason text NOT NULL DEFAULT '' CHECK (char_length(reason) <= 500),
  document jsonb NOT NULL CHECK (jsonb_typeof(document) = 'object'),
  signed_by text NOT NULL,
  issued_by text NOT NULL,
  issued_at timestamptz NOT NULL,
  idempotency_key text,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, quality_order_id, shipment_key, revision),
  UNIQUE (tenant_id, idempotency_key),
  CHECK ((revision = 1) = (supersedes IS NULL)),
  CHECK (revision = 1 OR reason <> ''),
  FOREIGN KEY (tenant_id, quality_order_id) REFERENCES quality_order (tenant_id, id),
  FOREIGN KEY (tenant_id, supersedes) REFERENCES quality_coa (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS quality_coa_order ON quality_coa (tenant_id, quality_order_id, issued_at, id);
ALTER TABLE quality_coa ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality_coa FORCE ROW LEVEL SECURITY;
CREATE POLICY quality_coa_tenant_access ON quality_coa AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
