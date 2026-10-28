-- Application role privileges. passage_app (no BYPASSRLS) works under the row-level
-- security policies; tables the store never updates or deletes stay SELECT, INSERT.
-- Skipped when the role does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON outbox, valuation_ledger, aging_snapshot, cost_transaction, cost_adjustment, reconciliation_run TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON idempotency_keys, costing_version, item_cost, valuation_category, aging_bucket_set TO passage_app';
  END IF;
END
$$;
