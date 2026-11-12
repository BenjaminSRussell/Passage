-- #29: contract modifications that remove or reduce obligations, take recognized revenue back out, apply to contracts credit notes
-- touched, and are invoiced by billing.
--
-- modification_reversal: the recognized revenue a modification takes back (a negative cumulative catch-up, or a price concession on
-- performance already transferred, IFRS 15.88 / ASC 606-10-32-43), one row per obligation, dated at the effective date. The
-- roll-forward reports it as `reversed` with the credit-note reversals, and POST /v1/revrec/journal-postings books it Dr 4000 / Cr 2400
-- with the same back-off and parking as a credit-note reversal (key revrec-modrev-<id>).
CREATE TABLE IF NOT EXISTS modification_reversal (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  contract_id uuid NOT NULL,
  modification_id uuid NOT NULL,
  obligation_id uuid NOT NULL,
  currency char(3) NOT NULL,
  posting_date date NOT NULL,
  revenue_reversed numeric(20,6) NOT NULL CHECK (revenue_reversed > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  ledger_id uuid,
  entry_id text,
  gl_attempt_count integer NOT NULL DEFAULT 0,
  gl_refusal_count integer NOT NULL DEFAULT 0,
  gl_next_attempt_at timestamptz,
  gl_last_error text,
  gl_parked_code text,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, modification_id, obligation_id),
  FOREIGN KEY (tenant_id, modification_id) REFERENCES contract_modification (tenant_id, id),
  FOREIGN KEY (tenant_id, contract_id) REFERENCES revenue_contract (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS modification_reversal_due_idx ON modification_reversal (tenant_id, legal_entity_id, posting_date) WHERE entry_id IS NULL;
ALTER TABLE modification_reversal ENABLE ROW LEVEL SECURITY;
ALTER TABLE modification_reversal FORCE ROW LEVEL SECURITY;
CREATE POLICY modification_reversal_tenant_access ON modification_reversal AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY modification_reversal_legal_entity_scope ON modification_reversal AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))
  WITH CHECK (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE contract_modification ADD COLUMN IF NOT EXISTS reversal_amount numeric(20,6) NOT NULL DEFAULT 0 CHECK (reversal_amount >= 0);
-- Obligations the modification removed or reduced: [{"obligationId", "scope"}].
ALTER TABLE contract_modification ADD COLUMN IF NOT EXISTS scope_changes jsonb NOT NULL DEFAULT '[]';

-- modification_billing: the price change of a modification, sent to billing's POST /v1/billing/contract-modifications (an invoice for
-- an increase, a credit note for a decrease) outside the modification's transaction, so a billing outage never blocks it. One row per
-- modification with a non-zero price change on a contract with an order; the body and Idempotency-Key are frozen, the reporter
-- (cmd/revrec, BILLING_URL) claims due rows with a lease and records BILLED, PENDING with a back-off, or PARKED.
CREATE TABLE IF NOT EXISTS modification_billing (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  modification_id uuid NOT NULL,
  contract_id uuid NOT NULL,
  idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 255),
  body jsonb NOT NULL,
  status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'BILLED', 'PARKED')),
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  last_status integer,
  last_error text CHECK (last_error IS NULL OR length(last_error) <= 1000),
  document_id uuid,
  billed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, modification_id),
  FOREIGN KEY (tenant_id, modification_id) REFERENCES contract_modification (tenant_id, id),
  CHECK ((status = 'BILLED') = (billed_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS modification_billing_pending_idx ON modification_billing (tenant_id, next_attempt_at) WHERE status = 'PENDING';
ALTER TABLE modification_billing ENABLE ROW LEVEL SECURITY;
ALTER TABLE modification_billing FORCE ROW LEVEL SECURITY;
CREATE POLICY modification_billing_tenant_access ON modification_billing AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY modification_billing_legal_entity_scope ON modification_billing AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The reporter finds the tenants with due rows through this cross-tenant index, kept by a trigger and read only by the SECURITY
-- DEFINER function (FORCE RLS binds the owner of modification_billing too). passage_app has no grant on the index.
CREATE TABLE IF NOT EXISTS modification_billing_due (
  tenant_id uuid NOT NULL,
  billing_id uuid NOT NULL,
  next_attempt_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, billing_id)
);
CREATE INDEX IF NOT EXISTS modification_billing_due_next_idx ON modification_billing_due (next_attempt_at);
DO $do$
BEGIN
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION modification_billing_track_due() RETURNS trigger
      LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
    BEGIN
      IF NEW.status = 'PENDING' THEN
        INSERT INTO modification_billing_due (tenant_id, billing_id, next_attempt_at) VALUES (NEW.tenant_id, NEW.id, NEW.next_attempt_at)
        ON CONFLICT (tenant_id, billing_id) DO UPDATE SET next_attempt_at = EXCLUDED.next_attempt_at;
      ELSE
        DELETE FROM modification_billing_due WHERE tenant_id = NEW.tenant_id AND billing_id = NEW.id;
      END IF;
      RETURN NEW;
    END
    $body$;
    CREATE OR REPLACE FUNCTION revrec_modification_billing_due_tenants() RETURNS SETOF uuid
      LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$ SELECT DISTINCT tenant_id FROM modification_billing_due WHERE next_attempt_at <= pg_catalog.now() $body$;
  $f$, current_schema());
END
$do$;
REVOKE ALL ON FUNCTION modification_billing_track_due() FROM PUBLIC;
REVOKE ALL ON FUNCTION revrec_modification_billing_due_tenants() FROM PUBLIC;
DROP TRIGGER IF EXISTS modification_billing_due ON modification_billing;
CREATE TRIGGER modification_billing_due AFTER INSERT OR UPDATE OF status, next_attempt_at ON modification_billing
  FOR EACH ROW EXECUTE FUNCTION modification_billing_track_due();

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON modification_reversal, modification_billing TO passage_app';
    EXECUTE 'GRANT UPDATE ON contract_modification TO passage_app';
    EXECUTE 'GRANT EXECUTE ON FUNCTION revrec_modification_billing_due_tenants() TO passage_app';
  END IF;
END
$$;
