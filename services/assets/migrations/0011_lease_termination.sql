-- Wave 37, D4c: early termination of a lease. One row per terminated lease records the
-- derecognition: the liability and the ROU asset (gross, accumulated depreciation and carrying
-- amount) that leave the books at the end of the last period in force, the termination payment
-- and the gain or loss. journal_id holds the ledger entry once the termination journal is booked
-- (lease-schedule posting, in period order after every period up to the termination).

CREATE TABLE IF NOT EXISTS lease_termination (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  lease_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  after_period integer NOT NULL CHECK (after_period >= 1),
  termination_date date NOT NULL,
  reason text NOT NULL DEFAULT '' CHECK (char_length(reason) <= 500),
  currency char(3) NOT NULL,
  liability_derecognized numeric(20,6) NOT NULL CHECK (liability_derecognized >= 0),
  rou_gross numeric(20,6) NOT NULL CHECK (rou_gross >= 0),
  rou_accumulated numeric(20,6) NOT NULL CHECK (rou_accumulated >= 0),
  rou_carrying numeric(20,6) NOT NULL CHECK (rou_carrying >= 0),
  termination_payment numeric(20,6) NOT NULL CHECK (termination_payment >= 0),
  gain_loss numeric(20,6) NOT NULL,
  journal_id uuid,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT lease_termination_one_per_lease UNIQUE (tenant_id, lease_id),
  FOREIGN KEY (tenant_id, lease_id) REFERENCES lease (tenant_id, id),
  CHECK (rou_gross = rou_accumulated + rou_carrying),
  CHECK (gain_loss = liability_derecognized - rou_carrying - termination_payment)
);
ALTER TABLE lease_termination ENABLE ROW LEVEL SECURITY;
ALTER TABLE lease_termination FORCE ROW LEVEL SECURITY;
CREATE POLICY lease_termination_tenant_access ON lease_termination AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY lease_termination_legal_entity_scope ON lease_termination AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON lease_termination TO passage_app';
  END IF;
END
$$;
