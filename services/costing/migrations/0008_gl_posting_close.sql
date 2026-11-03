-- Wave 20 slice 7: GL vouchers for PPV (receipt and invoice) and the inventory close job.
-- Additive only.

-- gr_ir_amount is the goods-received-not-invoiced clearing amount of a PPV row, stored by the
-- PPV hook, so the receipt and invoice vouchers derive from the stored row alone.
ALTER TABLE cost_transaction ADD COLUMN IF NOT EXISTS gr_ir_amount numeric(19,4);

-- The accounts a legal entity's vouchers post to, and the general ledger they post in.
CREATE TABLE IF NOT EXISTS costing_posting_profile (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  gl_ledger_id uuid NOT NULL,
  inventory_account text NOT NULL CHECK (char_length(inventory_account) BETWEEN 1 AND 40),
  grir_account text NOT NULL CHECK (char_length(grir_account) BETWEEN 1 AND 40),
  ap_account text NOT NULL CHECK (char_length(ap_account) BETWEEN 1 AND 40),
  ppv_account text NOT NULL CHECK (char_length(ppv_account) BETWEEN 1 AND 40),
  adjustment_account text NOT NULL CHECK (char_length(adjustment_account) BETWEEN 1 AND 40),
  version integer NOT NULL DEFAULT 1,
  updated_by text NOT NULL DEFAULT '',
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id)
);

-- A voucher is written here, with its exact lines, before the ledger is called; retries resend
-- this payload until the ledger confirms it (status POSTED), so a changed profile never changes
-- an entry the ledger may already hold. A failure counts an attempt and delays the next one with
-- backoff (next_attempt_at); after five attempts, or at once for an entry that cannot balance, the
-- voucher is PARKED with its reason code until the posting profile is saved again. rebuild marks a
-- voucher the ledger refused for good or that cannot balance: the next attempt derives its lines again
-- under a new key; any other parked voucher is resent under its own key. source_key is ctxn:<cost transaction id> or close:<close id>.
CREATE TABLE IF NOT EXISTS costing_gl_voucher (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  source_key text NOT NULL CHECK (char_length(source_key) BETWEEN 1 AND 200),
  gl_ledger_id uuid NOT NULL,
  currency char(3) NOT NULL,
  posting_date date NOT NULL,
  idempotency_key text NOT NULL,
  lines jsonb NOT NULL,
  status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'POSTED', 'PARKED')),
  journal_entry_id text,
  attempt_count integer NOT NULL DEFAULT 0,
  last_error text NOT NULL DEFAULT '',
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  parked_reason text,
  rebuild boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  posted_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT costing_gl_voucher_source UNIQUE (tenant_id, source_key)
);

-- One close per legal entity, valuation ledger (NULL is the primary valuation) and period end.
-- A close row, RUNNING or COMPLETED, locks the entity's cost transactions dated on or before
-- its period end.
CREATE TABLE IF NOT EXISTS inventory_close (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  valuation_ledger_id uuid,
  period_end date NOT NULL,
  method text NOT NULL,
  currency char(3) NOT NULL,
  status text NOT NULL DEFAULT 'RUNNING' CHECK (status IN ('RUNNING', 'COMPLETED')),
  groups integer NOT NULL DEFAULT 0,
  adjusted_groups integer NOT NULL DEFAULT 0,
  adjustment_total numeric(19,4) NOT NULL DEFAULT 0,
  journal_entry_id text,
  created_by text NOT NULL DEFAULT '',
  started_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  PRIMARY KEY (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS inventory_close_period
  ON inventory_close (tenant_id, legal_entity_id, COALESCE(valuation_ledger_id, '00000000-0000-0000-0000-000000000000'::uuid), period_end);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['costing_posting_profile', 'costing_gl_voucher', 'inventory_close'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('DROP POLICY IF EXISTS %I_tenant_access ON %I', t, t);
    EXECUTE format('CREATE POLICY %I_tenant_access ON %I AS PERMISSIVE
      USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)
      WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t, t);
    EXECUTE format('DROP POLICY IF EXISTS %I_legal_entity_scope ON %I', t, t);
    EXECUTE format('CREATE POLICY %I_legal_entity_scope ON %I AS RESTRICTIVE
      USING (current_setting(''passage.legal_entity_scopes'', true) = ''*''
             OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t, t);
  END LOOP;
END $$;

-- The profile, a close and a voucher are updated in place; nothing is deleted.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON costing_posting_profile, inventory_close TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON costing_gl_voucher TO passage_app';
  END IF;
END
$$;
