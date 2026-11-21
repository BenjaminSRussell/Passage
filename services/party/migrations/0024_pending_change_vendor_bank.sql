ALTER TABLE vendor_bank_account
  ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'active' CHECK (status IN ('pending', 'active', 'rejected'));

ALTER TABLE pending_change ALTER COLUMN customer_id DROP NOT NULL;
ALTER TABLE pending_change ALTER COLUMN requested_value DROP NOT NULL;
ALTER TABLE pending_change ADD COLUMN IF NOT EXISTS vendor_id uuid;
ALTER TABLE pending_change ADD COLUMN IF NOT EXISTS bank_account_id uuid;

ALTER TABLE pending_change DROP CONSTRAINT IF EXISTS pending_change_field_check;
ALTER TABLE pending_change DROP CONSTRAINT IF EXISTS pending_change_target_by_field;
ALTER TABLE pending_change
  ADD CONSTRAINT pending_change_target_by_field CHECK (
    (field = 'credit_limit' AND customer_id IS NOT NULL AND requested_value IS NOT NULL)
    OR (field = 'vendor_bank_account' AND vendor_id IS NOT NULL AND bank_account_id IS NOT NULL)
  );

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'pending_change_bank_account_fk') THEN
    ALTER TABLE pending_change
      ADD CONSTRAINT pending_change_bank_account_fk FOREIGN KEY (tenant_id, bank_account_id) REFERENCES vendor_bank_account (tenant_id, id);
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT UPDATE (status) ON vendor_bank_account TO depot_app';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'debezium_cdc_party') THEN
    EXECUTE 'GRANT SELECT (status) ON vendor_bank_account TO debezium_cdc_party';
  END IF;
END
$$;
