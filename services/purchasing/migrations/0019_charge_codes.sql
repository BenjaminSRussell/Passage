-- Wave 17 purchasing slice 15: charge codes and auto charges (FR-17.9). A charge code names a
-- kind of charge (freight, handling, customs) of a legal entity with its calculation, default
-- value, debit and credit sides and the rule that allocates a header charge to lines at invoice
-- time. An auto charge adds a code to the POs of a vendor, a vendor group or every vendor.
-- A PO charge keeps a copy of the sides it was entered with, so a later change to the code does
-- not move a placed PO. Additive only (expand/contract).
CREATE TABLE IF NOT EXISTS charge_code (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9_-]{1,20}$'),
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
  calc text NOT NULL CHECK (calc IN ('FIXED', 'PER_UNIT', 'PERCENT')),
  default_value numeric(20,6) NOT NULL DEFAULT 0 CHECK (default_value >= 0),
  debit_type text NOT NULL DEFAULT 'ITEM' CHECK (debit_type IN ('ITEM', 'LEDGER', 'VENDOR')),
  credit_type text NOT NULL DEFAULT 'VENDOR' CHECK (credit_type IN ('LEDGER', 'VENDOR')),
  debit_account_id uuid,
  credit_account_id uuid,
  vendor_id uuid,
  allocation_rule text NOT NULL DEFAULT 'AMOUNT' CHECK (allocation_rule IN ('QTY', 'AMOUNT', 'LINES')),
  status text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'INACTIVE')),
  row_version integer NOT NULL DEFAULT 1 CHECK (row_version > 0),
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK (debit_type <> 'LEDGER' OR debit_account_id IS NOT NULL),
  CHECK (credit_type <> 'LEDGER' OR credit_account_id IS NOT NULL),
  CHECK (debit_type <> 'VENDOR' OR vendor_id IS NOT NULL)
);
CREATE UNIQUE INDEX IF NOT EXISTS charge_code_unique ON charge_code (tenant_id, legal_entity_id, code);
CREATE INDEX IF NOT EXISTS charge_code_list ON charge_code (tenant_id, id DESC);

CREATE TABLE IF NOT EXISTS auto_charge (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  charge_code_id uuid NOT NULL,
  account_code text NOT NULL CHECK (account_code IN ('VENDOR', 'GROUP', 'ALL')),
  account_id uuid,
  value numeric(20,6) CHECK (value IS NULL OR value >= 0),
  min_order_amount numeric(20,6) CHECK (min_order_amount IS NULL OR min_order_amount > 0),
  currency char(3),
  valid_from date NOT NULL,
  valid_to date,
  status text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'INACTIVE')),
  row_version integer NOT NULL DEFAULT 1 CHECK (row_version > 0),
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, charge_code_id) REFERENCES charge_code (tenant_id, id),
  CHECK ((account_code = 'ALL') = (account_id IS NULL)),
  CHECK (min_order_amount IS NULL OR currency IS NOT NULL),
  CHECK (valid_to IS NULL OR valid_to >= valid_from)
);
CREATE INDEX IF NOT EXISTS auto_charge_list ON auto_charge (tenant_id, id DESC);
-- The auto charges a PO can take: its vendor's, its vendor groups' and the ALL rows of the legal entity.
CREATE INDEX IF NOT EXISTS auto_charge_match ON auto_charge (tenant_id, legal_entity_id, account_code, account_id) WHERE status = 'ACTIVE';

-- The sides a PO charge was entered with. Charges entered before this slice are capitalized into
-- the item cost, payable to the PO vendor and allocated by amount, as the PRD defaults.
ALTER TABLE purchase_order_charge ADD COLUMN IF NOT EXISTS charge_code_id uuid;
ALTER TABLE purchase_order_charge ADD COLUMN IF NOT EXISTS charge_code text NOT NULL DEFAULT '';
ALTER TABLE purchase_order_charge ADD COLUMN IF NOT EXISTS debit_type text NOT NULL DEFAULT 'ITEM' CHECK (debit_type IN ('ITEM', 'LEDGER', 'VENDOR'));
ALTER TABLE purchase_order_charge ADD COLUMN IF NOT EXISTS credit_type text NOT NULL DEFAULT 'VENDOR' CHECK (credit_type IN ('LEDGER', 'VENDOR'));
ALTER TABLE purchase_order_charge ADD COLUMN IF NOT EXISTS debit_account_id uuid;
ALTER TABLE purchase_order_charge ADD COLUMN IF NOT EXISTS credit_account_id uuid;
ALTER TABLE purchase_order_charge ADD COLUMN IF NOT EXISTS vendor_id uuid;
ALTER TABLE purchase_order_charge ADD COLUMN IF NOT EXISTS allocation_rule text NOT NULL DEFAULT 'AMOUNT' CHECK (allocation_rule IN ('QTY', 'AMOUNT', 'LINES'));
ALTER TABLE purchase_order_charge ADD COLUMN IF NOT EXISTS auto boolean NOT NULL DEFAULT false;

-- The charge codes auto charges have ever added to a PO. applyAutoCharges adds a code once: a buyer
-- who removes an auto-added charge keeps it removed on later edits.
ALTER TABLE purchase_order ADD COLUMN IF NOT EXISTS auto_charge_codes uuid[] NOT NULL DEFAULT '{}';

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['charge_code', 'auto_charge']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)',
                   t || '_tenant_access', t);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '',''))) WITH CHECK (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))',
                   t || '_legal_entity_scope', t);
  END LOOP;
END $$;

-- passage_app: a code and an auto charge are changed in place (UPDATE) and never deleted.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON charge_code, auto_charge TO passage_app';
  END IF;
END
$$;
