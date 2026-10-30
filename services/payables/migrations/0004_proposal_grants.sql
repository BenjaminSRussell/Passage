DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON ap_vendor_block TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON ap_payment_proposal, ap_payment_proposal_group, ap_payment_proposal_line TO passage_app';
    EXECUTE 'GRANT UPDATE (status, approved_by, approved_at) ON ap_payment_proposal TO passage_app';
  END IF;
END
$$;
