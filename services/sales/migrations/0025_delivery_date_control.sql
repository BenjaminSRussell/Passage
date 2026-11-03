-- Wave 27 slice 21: delivery date control (FR-27.3, BR-27.2).
-- sales_parameter gets delivery_date_control_default and sales_lead_time_days;
-- an order line may name its own control; a customer's transport days and
-- receiving weekdays live in customer_delivery_terms, one row per
-- (legal entity, customer). Confirm copies the customer's terms onto the
-- order (confirmed_transport_days, confirmed_receiving_days), and a line's
-- confirmed receipt date is its confirmed ship date dated by those copied
-- terms: a later change of the customer's terms does not move the receipt
-- date of an order that is already confirmed, while a re-promise of the ship
-- date carries its receipt date with it. NULL is an order confirmed before
-- this migration (no terms existed: receipt = ship); an empty array of
-- receiving days means every day.
-- ATP and CTP are valid values of the column (PRD) but the service refuses
-- them until the ATP projection and the CTP API exist.
ALTER TABLE sales_parameter
  ADD COLUMN IF NOT EXISTS delivery_date_control_default text NOT NULL DEFAULT 'NONE'
    CHECK (delivery_date_control_default IN ('NONE', 'SALES_LEAD_TIME', 'ATP', 'CTP')),
  ADD COLUMN IF NOT EXISTS sales_lead_time_days int NOT NULL DEFAULT 0
    CHECK (sales_lead_time_days BETWEEN 0 AND 365);

ALTER TABLE sales_order_line
  ADD COLUMN IF NOT EXISTS delivery_date_control text
    CHECK (delivery_date_control IN ('NONE', 'SALES_LEAD_TIME', 'ATP', 'CTP'));

ALTER TABLE sales_order
  ADD COLUMN IF NOT EXISTS confirmed_transport_days int CHECK (confirmed_transport_days BETWEEN 0 AND 60),
  ADD COLUMN IF NOT EXISTS confirmed_receiving_days smallint[]
    CHECK (cardinality(confirmed_receiving_days) <= 7 AND confirmed_receiving_days <@ ARRAY[1,2,3,4,5,6,7]::smallint[]);

CREATE TABLE IF NOT EXISTS customer_delivery_terms (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  transport_days int NOT NULL DEFAULT 0 CHECK (transport_days BETWEEN 0 AND 60),
  -- ISO weekdays, 1 Monday to 7 Sunday, sorted, at least one.
  receiving_days smallint[] NOT NULL CHECK (cardinality(receiving_days) BETWEEN 1 AND 7 AND receiving_days <@ ARRAY[1,2,3,4,5,6,7]::smallint[]),
  version int NOT NULL DEFAULT 1,
  idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 255),
  request_hash text NOT NULL,
  created_by text NOT NULL,
  updated_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  UNIQUE (tenant_id, legal_entity_id, customer_id)
);

ALTER TABLE customer_delivery_terms ENABLE ROW LEVEL SECURITY;
ALTER TABLE customer_delivery_terms FORCE ROW LEVEL SECURITY;
CREATE POLICY customer_delivery_terms_tenant_access ON customer_delivery_terms AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY customer_delivery_terms_legal_entity_scope ON customer_delivery_terms AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The store inserts a row and updates it by version; it never deletes.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON customer_delivery_terms TO passage_app';
  END IF;
END
$$;
