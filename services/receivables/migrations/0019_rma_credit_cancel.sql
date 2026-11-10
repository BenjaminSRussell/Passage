-- :cancel drops a PENDING RMA credit that was never posted, with its tax rows, so the RMA can be credited again (#557).
-- The store deletes only PENDING rows; a POSTED credit stays for good.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT DELETE ON rma_credit, rma_credit_tax TO passage_app';
  END IF;
END
$$;
