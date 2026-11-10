-- Wave 20 supplier portal, slice 511-1: a vendor's answer to a confirmed purchase order. A row is one answer to one
-- version: ACCEPTED, REJECTED with a reason, or CHANGES_PROPOSED with per-line changes that wait for the buyer
-- (decision APPROVED or DECLINED). At most one proposal per order is open, and a version is accepted or rejected once.
-- Additive only.
CREATE TABLE IF NOT EXISTS purchase_order_ack (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  purchase_order_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  version_no integer NOT NULL CHECK (version_no > 0),
  outcome text NOT NULL CHECK (outcome IN ('ACCEPTED', 'REJECTED', 'CHANGES_PROPOSED')),
  reason text NOT NULL DEFAULT '' CHECK (length(reason) <= 2000),
  changes jsonb NOT NULL DEFAULT '[]',
  acknowledged_by uuid,
  acknowledged_at timestamptz NOT NULL DEFAULT now(),
  decision text CHECK (decision IN ('APPROVED', 'DECLINED')),
  decided_by uuid,
  decided_at timestamptz,
  decision_note text NOT NULL DEFAULT '' CHECK (length(decision_note) <= 1000),
  resulting_version_no integer,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, purchase_order_id) REFERENCES purchase_order (tenant_id, id),
  CHECK (outcome = 'CHANGES_PROPOSED' OR decision IS NULL),
  CHECK ((decision IS NULL) = (decided_at IS NULL)),
  CHECK (outcome <> 'REJECTED' OR reason <> ''),
  CHECK (outcome <> 'CHANGES_PROPOSED' OR jsonb_array_length(changes) > 0)
);
CREATE UNIQUE INDEX IF NOT EXISTS purchase_order_ack_one_open
  ON purchase_order_ack (tenant_id, purchase_order_id) WHERE outcome = 'CHANGES_PROPOSED' AND decision IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS purchase_order_ack_answered
  ON purchase_order_ack (tenant_id, purchase_order_id, version_no) WHERE outcome IN ('ACCEPTED', 'REJECTED');
CREATE INDEX IF NOT EXISTS purchase_order_ack_list ON purchase_order_ack (tenant_id, id DESC);
CREATE INDEX IF NOT EXISTS purchase_order_ack_po ON purchase_order_ack (tenant_id, purchase_order_id, id DESC);

ALTER TABLE purchase_order_ack ENABLE ROW LEVEL SECURITY;
ALTER TABLE purchase_order_ack FORCE ROW LEVEL SECURITY;
CREATE POLICY purchase_order_ack_tenant_access ON purchase_order_ack AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY purchase_order_ack_legal_entity_scope ON purchase_order_ack AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*' OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))
  WITH CHECK (current_setting('passage.legal_entity_scopes', true) = '*' OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON purchase_order_ack TO passage_app';
  END IF;
END
$$;
