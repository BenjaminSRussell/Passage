CREATE TABLE IF NOT EXISTS partner_trade_profile (
  tenant_id uuid NOT NULL,
  partner_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  ship_from_name text NOT NULL CHECK (char_length(ship_from_name) BETWEEN 1 AND 60),
  ship_from_gln char(13) NOT NULL CHECK (ship_from_gln ~ '^[0-9]{13}$'),
  ship_to_name text NOT NULL CHECK (char_length(ship_to_name) BETWEEN 1 AND 60),
  ship_to_gln char(13) NOT NULL CHECK (ship_to_gln ~ '^[0-9]{13}$'),
  bill_to_name text NOT NULL CHECK (char_length(bill_to_name) BETWEEN 1 AND 60),
  bill_to_gln char(13) NOT NULL CHECK (bill_to_gln ~ '^[0-9]{13}$'),
  remit_to_name text NOT NULL CHECK (char_length(remit_to_name) BETWEEN 1 AND 60),
  remit_to_gln char(13) NOT NULL CHECK (remit_to_gln ~ '^[0-9]{13}$'),
  default_scac text NOT NULL CHECK (default_scac ~ '^[A-Z0-9]{2,4}$'),
  version integer NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, partner_id),
  FOREIGN KEY (tenant_id, partner_id) REFERENCES trading_partner (tenant_id, id)
);
ALTER TABLE partner_trade_profile ENABLE ROW LEVEL SECURITY;
ALTER TABLE partner_trade_profile FORCE ROW LEVEL SECURITY;
CREATE POLICY partner_trade_profile_tenant_access ON partner_trade_profile AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON partner_trade_profile TO passage_app';
  END IF;
END
$$;
