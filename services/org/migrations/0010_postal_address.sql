CREATE TABLE IF NOT EXISTS postal_address (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  address_id uuid NOT NULL,
  owner_type text NOT NULL CHECK (owner_type IN ('legal_entity', 'operating_unit', 'site', 'warehouse', 'location', 'party')),
  owner_id uuid NOT NULL,
  legal_entity_id uuid,
  country_code char(2) NOT NULL CHECK (country_code ~ '^[A-Z]{2}$'),
  region_code text CHECK (char_length(region_code) <= 10),
  city text NOT NULL CHECK (char_length(city) BETWEEN 1 AND 60),
  postal_code text CHECK (char_length(postal_code) <= 12),
  street_1 text NOT NULL CHECK (char_length(street_1) BETWEEN 1 AND 100),
  street_2 text CHECK (char_length(street_2) <= 100),
  street_3 text CHECK (char_length(street_3) <= 100),
  building text CHECK (char_length(building) <= 20),
  po_box text CHECK (char_length(po_box) <= 20),
  latitude numeric(9,6),
  longitude numeric(9,6),
  geo_status text NOT NULL DEFAULT 'unverified' CHECK (geo_status IN ('unverified', 'geocoded')),
  formatted text NOT NULL,
  valid_from date NOT NULL DEFAULT current_date,
  valid_to date,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (valid_to IS NULL OR valid_to > valid_from)
);
CREATE UNIQUE INDEX IF NOT EXISTS postal_address_current ON postal_address (tenant_id, address_id) WHERE valid_to IS NULL;
CREATE INDEX IF NOT EXISTS postal_address_owner ON postal_address (tenant_id, owner_type, owner_id);

CREATE TABLE IF NOT EXISTS address_purpose (
  tenant_id uuid NOT NULL,
  address_id uuid NOT NULL,
  owner_type text NOT NULL,
  owner_id uuid NOT NULL,
  legal_entity_id uuid,
  purpose text NOT NULL CHECK (purpose IN ('business', 'delivery', 'invoice', 'remit_to', 'service', 'home', 'other')),
  is_primary boolean NOT NULL DEFAULT false,
  PRIMARY KEY (tenant_id, address_id, purpose)
);
CREATE UNIQUE INDEX IF NOT EXISTS address_purpose_primary ON address_purpose (tenant_id, owner_type, owner_id, purpose) WHERE is_primary;

ALTER TABLE postal_address ENABLE ROW LEVEL SECURITY;
ALTER TABLE postal_address FORCE ROW LEVEL SECURITY;
CREATE POLICY postal_address_tenant_access ON postal_address AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY postal_address_legal_entity_scope ON postal_address AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id IS NULL
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE address_purpose ENABLE ROW LEVEL SECURITY;
ALTER TABLE address_purpose FORCE ROW LEVEL SECURITY;
CREATE POLICY address_purpose_tenant_access ON address_purpose AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY address_purpose_legal_entity_scope ON address_purpose AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id IS NULL
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
