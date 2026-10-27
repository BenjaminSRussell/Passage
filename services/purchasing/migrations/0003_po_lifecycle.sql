-- Wave 17 purchasing slice 3: purchase order lifecycle. Adds the versioned
-- change record (FR-17.8) and the received/invoiced quantities that bound
-- reductions (PRC-PO-007). Wave 18 writes the quantities; this slice reads them.

ALTER TABLE purchase_order_line
  ADD COLUMN IF NOT EXISTS received_qty numeric(20,6) NOT NULL DEFAULT 0 CHECK (received_qty >= 0),
  ADD COLUMN IF NOT EXISTS invoiced_qty numeric(20,6) NOT NULL DEFAULT 0 CHECK (invoiced_qty >= 0);

-- One row per confirmed version. snapshot is the full commercial content of the
-- PO at that version; patch is the RFC 6902 diff from the previous version (an
-- empty array for version 1). confirmed_at is null until the version is confirmed.
CREATE TABLE IF NOT EXISTS purchase_order_version (
  tenant_id uuid NOT NULL,
  purchase_order_id uuid NOT NULL,
  version_no integer NOT NULL CHECK (version_no > 0),
  snapshot jsonb NOT NULL,
  patch jsonb NOT NULL DEFAULT '[]'::jsonb,
  reapproval_required boolean NOT NULL DEFAULT false,
  confirmed_at timestamptz,
  confirmed_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, purchase_order_id, version_no),
  FOREIGN KEY (tenant_id, purchase_order_id) REFERENCES purchase_order (tenant_id, id)
);

ALTER TABLE purchase_order_version ENABLE ROW LEVEL SECURITY;
ALTER TABLE purchase_order_version FORCE ROW LEVEL SECURITY;
CREATE POLICY purchase_order_version_tenant_access ON purchase_order_version AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
