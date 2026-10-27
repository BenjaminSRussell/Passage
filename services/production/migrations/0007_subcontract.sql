-- Wave 21 slice 7: subcontract purchase requests (FR-21.10).
-- Additive (expand): no existing row changes.

-- When release asked purchasing for the vendor PO of a subcontract operation
-- (event subcontract.porequested). Set once, so a reset and a second release, a
-- bulk retry or an edit of the released order never request the same operation
-- twice. The PO line id that purchasing (wave 17) creates is written back by a
-- later slice; until then this is the operation's PO linkage. An edit of a
-- released order that turns subcontracting off keeps the timestamp: the PO to
-- cancel is purchasing's (see NOT-IMPLEMENTED.md).
ALTER TABLE production_operation ADD COLUMN IF NOT EXISTS po_requested_at timestamptz;

-- The subcontract operations inquiry (PRD 4.21.14) reads only subcontract rows.
CREATE INDEX IF NOT EXISTS production_operation_subcontract ON production_operation (tenant_id, order_id, oper_number) WHERE subcontract;
