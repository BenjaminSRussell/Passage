INSERT INTO budget_plan_scenario_editor (tenant_id, legal_entity_id, scenario_id, actor)
SELECT tenant_id, legal_entity_id, id, updated_by FROM budget_plan_scenario WHERE updated_by IS NOT NULL
ON CONFLICT DO NOTHING;
