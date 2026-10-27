-- Billing slice 5: invoices from posted packing slips (FR-29.1), consolidated
-- invoices (FR-29.2), the source references the audit trace reads (FR-29.12)
-- and the rule that a packing slip line is invoiced at most once (BR-29.5).

-- One POST /v1/billing/proposals request. Its Idempotency-Key names the whole
-- batch; a replay returns every invoice the batch created.
CREATE TABLE IF NOT EXISTS proposal_batch (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT proposal_batch_key_unique UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE proposal_batch ENABLE ROW LEVEL SECURITY;
ALTER TABLE proposal_batch FORCE ROW LEVEL SECURITY;
CREATE POLICY proposal_batch_tenant_access ON proposal_batch AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY proposal_batch_legal_entity_scope ON proposal_batch AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE invoice ADD COLUMN IF NOT EXISTS proposal_batch_id uuid;
ALTER TABLE invoice ADD COLUMN IF NOT EXISTS proposal_batch_seq integer CHECK (proposal_batch_seq > 0);
ALTER TABLE invoice ADD CONSTRAINT invoice_proposal_batch_fk
  FOREIGN KEY (tenant_id, proposal_batch_id) REFERENCES proposal_batch (tenant_id, id);
ALTER TABLE invoice ADD CONSTRAINT invoice_proposal_batch_pair
  CHECK ((proposal_batch_id IS NULL) = (proposal_batch_seq IS NULL));
ALTER TABLE invoice ADD CONSTRAINT invoice_proposal_batch_shipment
  CHECK (proposal_batch_id IS NULL OR invoice_type IN ('SHIPMENT', 'CONSOLIDATED'));
CREATE UNIQUE INDEX IF NOT EXISTS invoice_proposal_batch_idx ON invoice (tenant_id, proposal_batch_id, proposal_batch_seq)
  WHERE proposal_batch_id IS NOT NULL;

-- §5.29.4 invoice_line source fields. The packing slip and order rows belong to
-- fulfillment (wave 28) and sales (wave 27); billing keeps their ids.
ALTER TABLE invoice_line
  ADD COLUMN IF NOT EXISTS product_id uuid,
  ADD COLUMN IF NOT EXISTS order_id uuid,
  ADD COLUMN IF NOT EXISTS order_line_id uuid,
  ADD COLUMN IF NOT EXISTS packing_slip_id uuid,
  ADD COLUMN IF NOT EXISTS packing_slip_line_id uuid,
  ADD COLUMN IF NOT EXISTS packing_slip_date date,
  ADD COLUMN IF NOT EXISTS price_explain_ref text CHECK (length(price_explain_ref) BETWEEN 1 AND 200);
-- A shipment line names its slip, slip line, slip date, order, order line and product together.
ALTER TABLE invoice_line ADD CONSTRAINT invoice_line_shipment_source
  CHECK ((packing_slip_line_id IS NULL AND packing_slip_id IS NULL AND packing_slip_date IS NULL
          AND order_id IS NULL AND order_line_id IS NULL AND product_id IS NULL AND price_explain_ref IS NULL)
      OR (packing_slip_line_id IS NOT NULL AND packing_slip_id IS NOT NULL AND packing_slip_date IS NOT NULL
          AND order_id IS NOT NULL AND order_line_id IS NOT NULL AND product_id IS NOT NULL));

-- BR-29.5: a packing slip line belongs to at most one invoice or proposal.
CREATE UNIQUE INDEX IF NOT EXISTS invoice_line_slip_line_unique ON invoice_line (tenant_id, packing_slip_line_id)
  WHERE packing_slip_line_id IS NOT NULL;
