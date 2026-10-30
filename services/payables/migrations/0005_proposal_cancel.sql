ALTER TABLE ap_payment_proposal DROP CONSTRAINT IF EXISTS ap_payment_proposal_status_check;
ALTER TABLE ap_payment_proposal ADD CONSTRAINT ap_payment_proposal_status_check CHECK (status IN ('DRAFT', 'APPROVED', 'CANCELLED'));
ALTER TABLE ap_payment_proposal ADD COLUMN IF NOT EXISTS cancelled_by text;
ALTER TABLE ap_payment_proposal ADD COLUMN IF NOT EXISTS cancelled_at timestamptz;
ALTER TABLE ap_payment_proposal DROP CONSTRAINT IF EXISTS ap_payment_proposal_cancelled_check;
ALTER TABLE ap_payment_proposal ADD CONSTRAINT ap_payment_proposal_cancelled_check
  CHECK ((status = 'CANCELLED') = (cancelled_by IS NOT NULL AND cancelled_at IS NOT NULL) AND (cancelled_by IS NULL OR cancelled_by <> ''));

ALTER TABLE ap_payment_proposal_line ADD COLUMN IF NOT EXISTS reserved boolean NOT NULL DEFAULT true;
ALTER TABLE ap_payment_proposal_line DROP CONSTRAINT IF EXISTS ap_payment_proposal_line_pkey;
ALTER TABLE ap_payment_proposal_line ADD PRIMARY KEY (tenant_id, proposal_id, open_item_id);
CREATE UNIQUE INDEX IF NOT EXISTS ap_payment_proposal_line_reserved ON ap_payment_proposal_line (tenant_id, open_item_id) WHERE reserved;

CREATE OR REPLACE FUNCTION ap_payment_proposal_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  -- APPROVED is terminal until the execution states of #102 add their own transitions; CANCELLED is terminal for good.
  IF OLD.status <> 'DRAFT' THEN
    RAISE EXCEPTION 'payment proposal % is % and can no longer change', OLD.id, OLD.status USING ERRCODE = '55000';
  END IF;
  IF NEW.created_by IS DISTINCT FROM OLD.created_by THEN
    RAISE EXCEPTION 'payment proposal creator cannot change' USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END
$$;
DROP TRIGGER IF EXISTS ap_payment_proposal_guard ON ap_payment_proposal;
CREATE TRIGGER ap_payment_proposal_guard BEFORE UPDATE ON ap_payment_proposal FOR EACH ROW EXECUTE FUNCTION ap_payment_proposal_guard();

CREATE OR REPLACE FUNCTION ap_payment_proposal_line_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.reserved AND NOT OLD.reserved THEN
    RAISE EXCEPTION 'a released proposal line cannot be reserved again' USING ERRCODE = '55000';
  END IF;
  IF OLD.reserved AND NOT NEW.reserved AND NOT EXISTS (
    SELECT 1 FROM ap_payment_proposal p WHERE p.tenant_id = NEW.tenant_id AND p.id = NEW.proposal_id AND p.status = 'CANCELLED') THEN
    RAISE EXCEPTION 'a proposal line is released only by cancelling its proposal' USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END
$$;
DROP TRIGGER IF EXISTS ap_payment_proposal_line_guard ON ap_payment_proposal_line;
CREATE TRIGGER ap_payment_proposal_line_guard BEFORE UPDATE ON ap_payment_proposal_line FOR EACH ROW EXECUTE FUNCTION ap_payment_proposal_line_guard();

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT UPDATE (cancelled_by, cancelled_at) ON ap_payment_proposal TO passage_app';
    EXECUTE 'GRANT UPDATE (reserved) ON ap_payment_proposal_line TO passage_app';
  END IF;
END
$$;
