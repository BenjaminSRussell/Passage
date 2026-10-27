-- Wave 37 slice 4: the mid-quarter convention on declining-balance books
-- (FR-37.2, BR-37.5) and the capex request decisions (FR-37.17, BR-37.12).
-- Expand-only: existing rows keep their meaning. The new table carries
-- tenant_id first, ENABLE and FORCE row-level security, the tenant policy and
-- the legal-entity scope.

ALTER TABLE asset_book DROP CONSTRAINT IF EXISTS asset_book_convention_check;
ALTER TABLE asset_book ADD CONSTRAINT asset_book_convention_check
  CHECK (convention IN ('None', 'FullMonth', 'MidMonth15', 'HalfYear', 'MidQuarter'));

-- created_by is the principal that created the request; the approver's
-- decisions are refused for that principal (segregation of duties, 7.37.17).
-- Requests created before this migration have no recorded creator.
ALTER TABLE capex_request ADD COLUMN IF NOT EXISTS created_by text;

-- One row per decision on a request (approve, reject, postpone, reopen,
-- release, close): who moved it, from which status to which, and why. This is
-- the approver trail the capex approval record (7.37.15) prints.
CREATE TABLE IF NOT EXISTS capex_request_decision (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  request_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  decision text NOT NULL CHECK (decision IN ('approve', 'reject', 'postpone', 'reopen', 'release', 'close')),
  from_status text NOT NULL,
  to_status text NOT NULL,
  approval_level text CHECK (approval_level IN ('PlantManager', 'CFO', 'Board')),
  amount numeric(20,6),
  comment text CHECK (comment IS NULL OR char_length(comment) BETWEEN 1 AND 1000),
  decided_by text NOT NULL,
  decided_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, request_id) REFERENCES capex_request (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS capex_request_decision_request ON capex_request_decision (tenant_id, request_id, decided_at);
ALTER TABLE capex_request_decision ENABLE ROW LEVEL SECURITY;
ALTER TABLE capex_request_decision FORCE ROW LEVEL SECURITY;
CREATE POLICY capex_request_decision_tenant_access ON capex_request_decision AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY capex_request_decision_legal_entity_scope ON capex_request_decision AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
