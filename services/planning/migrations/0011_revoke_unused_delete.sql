-- Keeps DELETE on plan_version, planned_order, net_requirement, pegging and
-- action_message (version purge in store.purgeVersions) and on quota_source
-- (replaced on quota arrangement update). passage_app loses DELETE on the rest:
-- plan, forecast_model, forecast_line, coverage_group, item_coverage, buffer_profile,
-- ddmrp_buffer and quota_arrangement. Skipped when the role does not exist
-- (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'REVOKE DELETE ON plan, forecast_model, forecast_line, coverage_group, item_coverage, buffer_profile, ddmrp_buffer, quota_arrangement FROM passage_app';
  END IF;
END
$$;
