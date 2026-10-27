-- Wave 27 slice 6: product proposal rules and the responses recorded against
-- them (FR-27.21, SAP VB41 cross-selling and dynamic product proposal).
-- A rule proposes proposed_product_id when ordered_product_id is committed on
-- an order, for one customer or (customer NULL) every customer of the legal
-- entity. Proposals create no line; a user's accept or decline is stored in
-- product_proposal_response so the acceptance rate can be read per rule.

CREATE TABLE IF NOT EXISTS product_proposal_rule (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  ordered_product_id uuid NOT NULL,
  proposed_product_id uuid NOT NULL,
  customer_id uuid,
  reason text NOT NULL CHECK (reason IN ('CROSS_SELL', 'UP_SELL', 'ACCESSORY')),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK (ordered_product_id <> proposed_product_id)
);
-- One rule per (ordered product, customer or all customers, proposed product),
-- so a product is never proposed twice for one reason and another.
CREATE UNIQUE INDEX IF NOT EXISTS product_proposal_rule_unique ON product_proposal_rule
  (tenant_id, legal_entity_id, ordered_product_id, coalesce(customer_id, '00000000-0000-0000-0000-000000000000'::uuid), proposed_product_id);

ALTER TABLE product_proposal_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE product_proposal_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY product_proposal_rule_tenant_access ON product_proposal_rule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY product_proposal_rule_legal_entity_scope ON product_proposal_rule AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- One row per user response to a proposal. The rule's products and reason are
-- copied so the history survives the rule's removal; rule_id is therefore not
-- a foreign key. A repeat of the Idempotency-Key returns the stored row.
CREATE TABLE IF NOT EXISTS product_proposal_response (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  rule_id uuid NOT NULL,
  ordered_product_id uuid NOT NULL,
  proposed_product_id uuid NOT NULL,
  reason text NOT NULL,
  customer_id uuid,
  order_id uuid,
  accepted boolean NOT NULL,
  idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 255),
  request_hash text NOT NULL,
  responded_by text NOT NULL,
  responded_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS product_proposal_response_rule ON product_proposal_response (tenant_id, rule_id);

ALTER TABLE product_proposal_response ENABLE ROW LEVEL SECURITY;
ALTER TABLE product_proposal_response FORCE ROW LEVEL SECURITY;
CREATE POLICY product_proposal_response_tenant_access ON product_proposal_response AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY product_proposal_response_legal_entity_scope ON product_proposal_response AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
