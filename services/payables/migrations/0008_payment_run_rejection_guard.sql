ALTER TABLE ap_payment_run ADD CONSTRAINT ap_payment_run_rejected_check
  CHECK (journal_rejected_at IS NULL OR journal_requested_at IS NOT NULL);

CREATE OR REPLACE FUNCTION ap_payment_run_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE p record;
BEGIN
  IF TG_OP = 'INSERT' THEN
    SELECT created_by, approved_by INTO p FROM ap_payment_proposal WHERE tenant_id = NEW.tenant_id AND id = NEW.proposal_id;
    IF lower(btrim(NEW.requested_by)) IN (lower(btrim(p.created_by)), lower(btrim(p.approved_by))) THEN
      RAISE EXCEPTION 'a payment run must be started by a user who neither created nor approved the proposal' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
  END IF;
  IF (OLD.journal_requested_at IS NOT NULL AND NEW.journal_requested_at IS DISTINCT FROM OLD.journal_requested_at)
     OR (OLD.journal_posted_at IS NOT NULL AND NEW.journal_posted_at IS DISTINCT FROM OLD.journal_posted_at)
     OR (OLD.journal_entry_id IS NOT NULL AND NEW.journal_entry_id IS DISTINCT FROM OLD.journal_entry_id) THEN
    RAISE EXCEPTION 'the payment journal of run % cannot be cleared or changed' ,OLD.id USING ERRCODE = '55000';
  END IF;
  IF OLD.journal_rejected_at IS NOT NULL AND NEW.journal_rejected_at IS DISTINCT FROM OLD.journal_rejected_at THEN
    RAISE EXCEPTION 'the journal rejection of run % cannot be cleared or changed', OLD.id USING ERRCODE = '55000';
  END IF;
  IF OLD.journal_rejected_at IS NULL AND NEW.journal_rejected_at IS NOT NULL
     AND (NEW.journal_requested_at IS NULL OR NEW.journal_posted_at IS NOT NULL) THEN
    RAISE EXCEPTION 'run % can be marked rejected only after its journal was requested and before it posted', OLD.id USING ERRCODE = '55000';
  END IF;
  IF OLD.status <> 'RUNNING' AND NEW.status IS DISTINCT FROM OLD.status THEN
    RAISE EXCEPTION 'payment run % is % and can no longer change status', OLD.id, OLD.status USING ERRCODE = '55000';
  END IF;
  IF OLD.status <> 'RUNNING' AND (NEW.failure_reason IS DISTINCT FROM OLD.failure_reason OR NEW.completed_at IS DISTINCT FROM OLD.completed_at) THEN
    RAISE EXCEPTION 'payment run % is % and can no longer change' , OLD.id, OLD.status USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END
$$;
