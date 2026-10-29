-- Application role privileges. passage_app (no BYPASSRLS) works under the row-level
-- security policies. Tables the service updates carry UPDATE; the rest are insert-only.
-- Skipped when the role does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON outbox, quality_test_group, quality_association, quality_result, quality_usage_decision, quality_compliance_list, quality_item_composition, quality_compliance_result, quality_certificate, quality_sample, quality_receipt_check, quality_coa TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON quality_pull_point TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON quality_stability_study TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON quality_order TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON quality_instrument TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON quality_nonconformance TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON quality_correction TO passage_app';
  END IF;
END
$$;
