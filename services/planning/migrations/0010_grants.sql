-- Application role privileges. passage_app (no BYPASSRLS) works under the row-level
-- security policies. outbox and firming_batch are append-only. Skipped when the role
-- does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON outbox, firming_batch TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON plan, forecast_model, forecast_line, plan_version, planned_order, net_requirement, pegging, action_message, coverage_group, item_coverage, buffer_profile, ddmrp_buffer, quota_arrangement, quota_source TO passage_app';
  END IF;
END
$$;
