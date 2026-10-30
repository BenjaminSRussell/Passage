DO $$
DECLARE c record;
BEGIN
  FOR c IN SELECT conname FROM pg_constraint
    WHERE conrelid = 'ap_payment_proposal'::regclass AND contype = 'c' AND pg_get_constraintdef(oid) LIKE '%approved_by IS NOT NULL%'
  LOOP
    EXECUTE format('ALTER TABLE ap_payment_proposal DROP CONSTRAINT %I', c.conname);
  END LOOP;
END
$$;
ALTER TABLE ap_payment_proposal DROP CONSTRAINT IF EXISTS ap_payment_proposal_status_check;
ALTER TABLE ap_payment_proposal ADD CONSTRAINT ap_payment_proposal_status_check
  CHECK (status IN ('DRAFT', 'APPROVED', 'CANCELLED', 'EXECUTING', 'EXECUTED', 'FAILED'));
ALTER TABLE ap_payment_proposal ADD CONSTRAINT ap_payment_proposal_approved_check
  CHECK ((status IN ('APPROVED', 'EXECUTING', 'EXECUTED', 'FAILED')) = (approved_by IS NOT NULL AND approved_at IS NOT NULL));
ALTER TABLE ap_payment_proposal ADD COLUMN IF NOT EXISTS executed_at timestamptz;
ALTER TABLE ap_payment_proposal ADD COLUMN IF NOT EXISTS failed_at timestamptz;
ALTER TABLE ap_payment_proposal ADD CONSTRAINT ap_payment_proposal_executed_check CHECK ((status = 'EXECUTED') = (executed_at IS NOT NULL));
ALTER TABLE ap_payment_proposal ADD CONSTRAINT ap_payment_proposal_failed_check CHECK ((status = 'FAILED') = (failed_at IS NOT NULL));

CREATE OR REPLACE FUNCTION ap_payment_proposal_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.created_by IS DISTINCT FROM OLD.created_by THEN
    RAISE EXCEPTION 'payment proposal creator cannot change' USING ERRCODE = '55000';
  END IF;
  IF OLD.status <> 'DRAFT' AND (NEW.approved_by IS DISTINCT FROM OLD.approved_by OR NEW.approved_at IS DISTINCT FROM OLD.approved_at) THEN
    RAISE EXCEPTION 'payment proposal approval cannot change' USING ERRCODE = '55000';
  END IF;
  IF NOT ((OLD.status, NEW.status) IN (('DRAFT', 'DRAFT'), ('DRAFT', 'APPROVED'), ('DRAFT', 'CANCELLED'),
                                       ('APPROVED', 'EXECUTING'), ('EXECUTING', 'EXECUTED'), ('EXECUTING', 'FAILED'))) THEN
    RAISE EXCEPTION 'payment proposal % is % and can no longer change to %', OLD.id, OLD.status, NEW.status USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION ap_payment_proposal_line_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.reserved AND NOT OLD.reserved THEN
    RAISE EXCEPTION 'a released proposal line cannot be reserved again' USING ERRCODE = '55000';
  END IF;
  IF OLD.reserved AND NOT NEW.reserved AND NOT EXISTS (
    SELECT 1 FROM ap_payment_proposal p WHERE p.tenant_id = NEW.tenant_id AND p.id = NEW.proposal_id AND p.status IN ('CANCELLED', 'FAILED')) THEN
    RAISE EXCEPTION 'a proposal line is released only by cancelling or failing its proposal' USING ERRCODE = '55000';
  END IF;
  IF OLD.settled AND NOT NEW.settled THEN
    RAISE EXCEPTION 'a settled proposal line cannot be unsettled' USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END
$$;

ALTER TABLE ap_payment_proposal_line ADD COLUMN IF NOT EXISTS settled boolean NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS ap_payment_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  proposal_id uuid NOT NULL,
  status text NOT NULL CHECK (status IN ('RUNNING', 'COMPLETED', 'FAILED')),
  requested_by text NOT NULL CHECK (requested_by <> ''),
  request_hash text NOT NULL,
  ledger_id uuid NOT NULL,
  bank_account text NOT NULL CHECK (bank_account <> ''),
  control_account text NOT NULL CHECK (control_account <> ''),
  discount_account text,
  file_request jsonb NOT NULL,
  file_number text,
  file_content text,
  file_sha256 text,
  payment_count integer,
  file_generated_at timestamptz,
  journal_entry_id text,
  journal_posted_at timestamptz,
  settled_at timestamptz,
  failure_reason text,
  created_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, proposal_id),
  FOREIGN KEY (tenant_id, proposal_id) REFERENCES ap_payment_proposal (tenant_id, id),
  CHECK ((file_generated_at IS NULL) = (file_content IS NULL)),
  CHECK ((journal_posted_at IS NULL) = (journal_entry_id IS NULL)),
  CHECK (journal_posted_at IS NULL OR file_generated_at IS NOT NULL),
  CHECK (settled_at IS NULL OR journal_posted_at IS NOT NULL),
  CHECK ((status = 'COMPLETED') = (completed_at IS NOT NULL)),
  CHECK (status <> 'COMPLETED' OR settled_at IS NOT NULL),
  CHECK ((status = 'FAILED') = (failure_reason IS NOT NULL)),
  CHECK (status <> 'FAILED' OR journal_posted_at IS NULL)
);
ALTER TABLE ap_payment_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE ap_payment_run FORCE ROW LEVEL SECURITY;
CREATE POLICY ap_payment_run_tenant_access ON ap_payment_run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY ap_payment_run_legal_entity_scope ON ap_payment_run AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS ap_payment (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  run_id uuid NOT NULL,
  group_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  payment_method text NOT NULL,
  amount numeric(20,6) NOT NULL CHECK (amount > 0),
  valued_amount numeric(20,6) NOT NULL,
  end_to_end_id text NOT NULL CHECK (end_to_end_id <> ''),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, group_id),
  UNIQUE (tenant_id, run_id, end_to_end_id),
  FOREIGN KEY (tenant_id, run_id) REFERENCES ap_payment_run (tenant_id, id),
  FOREIGN KEY (tenant_id, group_id) REFERENCES ap_payment_proposal_group (tenant_id, id)
);
ALTER TABLE ap_payment ENABLE ROW LEVEL SECURITY;
ALTER TABLE ap_payment FORCE ROW LEVEL SECURITY;
CREATE POLICY ap_payment_tenant_access ON ap_payment AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY ap_payment_legal_entity_scope ON ap_payment AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT UPDATE (executed_at, failed_at) ON ap_payment_proposal TO passage_app';
    EXECUTE 'GRANT UPDATE (settled) ON ap_payment_proposal_line TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON ap_payment_run, ap_payment TO passage_app';
    EXECUTE 'GRANT UPDATE (status, file_number, file_content, file_sha256, payment_count, file_generated_at, journal_entry_id, journal_posted_at, settled_at, failure_reason, completed_at) ON ap_payment_run TO passage_app';
  END IF;
END
$$;
