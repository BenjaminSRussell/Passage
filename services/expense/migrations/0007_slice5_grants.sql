-- Application role privileges for the slice-5 tables (0004-0006), on the same terms as
-- 0003_grants.sql: requisitions and advances change status, so they carry UPDATE; card
-- imports and card transactions are insert-only until assignment to a report is built.
-- Skipped when the role does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON travel_requisition, cash_advance TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON card_import, card_transaction TO passage_app';
  END IF;
END
$$;
