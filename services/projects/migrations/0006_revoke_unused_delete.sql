-- Keeps DELETE on wbs_node and wbs_dependency (plan replace in store/plans.go).
-- passage_app loses DELETE on project, project_group, posting_profile, plan_version,
-- project_change_order and project_risk_issue. project_change_order_line is removed
-- only by ON DELETE CASCADE, which runs as the table owner. Skipped when the role
-- does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'REVOKE DELETE ON project, project_group, posting_profile, plan_version, project_change_order, project_risk_issue FROM passage_app';
  END IF;
END
$$;
