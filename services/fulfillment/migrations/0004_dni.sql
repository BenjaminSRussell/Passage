-- Fulfillment slice 4: the delivered-not-invoiced report (FR-28.12) reads the
-- 1405 lines of the slips of one legal entity that are not invoiced. Invoiced
-- slips are the bulk of history, so the partial index leaves them out.
CREATE INDEX IF NOT EXISTS packing_slip_dni_idx ON packing_slip (tenant_id, legal_entity_id, posting_date)
  WHERE status <> 'INVOICED';
