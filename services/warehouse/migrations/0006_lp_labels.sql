-- #32 (32-3): the warehouse generates the license plate id and the SSCC of a pallet when it is received, from the
-- tenant's GS1 company prefix, and keeps the lot and expiry date the label shows. The ASN may leave the license
-- plate id out (the warehouse then uses the SSCC as the id); an id the ASN names stays.

CREATE TABLE IF NOT EXISTS gs1_company (
  tenant_id uuid NOT NULL PRIMARY KEY,
  -- A GS1 company prefix is 7 to 11 digits; with the extension digit and the serial reference it fills 16 digits.
  company_prefix text NOT NULL CHECK (company_prefix ~ '^[0-9]{7,11}$'),
  extension integer NOT NULL DEFAULT 0 CHECK (extension BETWEEN 0 AND 9),
  next_serial bigint NOT NULL DEFAULT 1 CHECK (next_serial > 0),
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE gs1_company ENABLE ROW LEVEL SECURITY;
ALTER TABLE gs1_company FORCE ROW LEVEL SECURITY;
CREATE POLICY gs1_company_tenant_access ON gs1_company AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE receipt_line ADD COLUMN IF NOT EXISTS sscc text NOT NULL DEFAULT '' CHECK (sscc = '' OR sscc ~ '^[0-9]{18}$');
ALTER TABLE receipt_line ADD COLUMN IF NOT EXISTS lot text NOT NULL DEFAULT '' CHECK (char_length(lot) <= 40);
ALTER TABLE receipt_line ADD COLUMN IF NOT EXISTS expiry_date date;

-- A blank license plate id is allowed until the pallet is received; the ids that are named stay unique per receipt.
DO $$
DECLARE c record;
BEGIN
  FOR c IN
    SELECT conname FROM pg_constraint
    WHERE conrelid = 'receipt_line'::regclass
      AND ((contype = 'c' AND pg_get_constraintdef(oid) LIKE '%lp_id%char_length%')
        OR (contype = 'u' AND pg_get_constraintdef(oid) LIKE '%lp_id%'))
  LOOP
    EXECUTE format('ALTER TABLE receipt_line DROP CONSTRAINT %I', c.conname);
  END LOOP;
END
$$;
ALTER TABLE receipt_line ADD CONSTRAINT receipt_line_lp_id_length CHECK (char_length(lp_id) BETWEEN 0 AND 40);
CREATE UNIQUE INDEX IF NOT EXISTS receipt_line_lp_id_unique ON receipt_line (tenant_id, receipt_id, lp_id) WHERE lp_id <> '';
-- An SSCC identifies one pallet in the world: never twice in a tenant.
CREATE UNIQUE INDEX IF NOT EXISTS receipt_line_sscc_unique ON receipt_line (tenant_id, sscc) WHERE sscc <> '';

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON gs1_company TO passage_app';
  END IF;
END
$$;
