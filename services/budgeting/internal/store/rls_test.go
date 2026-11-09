package store

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/budgeting/migrations"
)

func TestAppRole_CrossTenantRLS(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	testkit.AssertTenantRLS(t, admin)
	testkit.CrossTenant(t, app, []testkit.RLSCase{
		testkit.OutboxCase(),
		testkit.IdempotencyCase(),
		{
			Table:       "budget_control_rule",
			Insert:      `insert into budget_control_rule (tenant_id, id, legal_entity_id, code, version, warn_pct, block_pct, currency, effective_from) values ($1, $2, $3, 'DEPT', 1, 0, 5, 'USD', '2026-01-01')`,
			Args:        func(tenantID, entityID string) []any { return []any{tenantID, ids.New(), entityID} },
			LegalEntity: true,
			NoUpdate:    true,
		},
		planCase("budget_plan_process", planProcessInsert),
		planCase("budget_plan_scenario", planScenarioInsert),
		planCase("budget_plan_cell", planCellInsert),
		planCase("budget_plan_stage_owner", planOwnerInsert),
		planCase("budget_plan_decision", planDecisionInsert),
	})
}

const (
	planRuleCTE = `with r as (insert into budget_control_rule (tenant_id, id, legal_entity_id, code, version, warn_pct, block_pct, currency, effective_from)
	values ($1, $3, $2, 'P' || upper(right(replace($6::text, '-', ''), 12)), 1, 0, 5, 'USD', '2026-01-01') returning id)`
	planProcessCTE = planRuleCTE + `, p as (insert into budget_plan_process (tenant_id, id, legal_entity_id, rule_version_id, name, fiscal_year, currency, created_by)
	select $1, $4, $2, id, 'FY27-' || right($6::text, 12), 2027, 'USD', 'u' from r returning id)`
	planScenarioCTE = planProcessCTE + `, s as (insert into budget_plan_scenario (tenant_id, id, legal_entity_id, process_id, name, kind)
	select $1, $5, $2, id, 'Baseline', 'Baseline' from p returning id)`

	planProcessInsert  = planProcessCTE + ` select 1 where $5::text is not null`
	planScenarioInsert = planScenarioCTE + ` select 1`
	planCellInsert     = planScenarioCTE + ` insert into budget_plan_cell (tenant_id, legal_entity_id, process_id, scenario_id, account, cost_center, period, amount) select $1, $2, $4, id, '6000', 'CC1', '2027-01', 1 from s`
	planOwnerInsert    = planScenarioCTE + ` insert into budget_plan_stage_owner (tenant_id, legal_entity_id, process_id, stage, org_unit_id, owner) select $1, $2, $4, 'Review', $5, 'rev' from s`
	planDecisionInsert = planScenarioCTE + ` insert into budget_plan_decision (tenant_id, id, legal_entity_id, process_id, submission, stage, actor, decision) select $1, $5, $2, $4, 1, 'Submit', 'u', 'Submitted' from s`
)

func planCase(table, insert string) testkit.RLSCase {
	return testkit.RLSCase{
		Table:  table,
		Insert: insert,
		Args: func(tenantID, entityID string) []any {
			return []any{tenantID, entityID, ids.New(), ids.New(), ids.New(), ids.New()}
		},
		LegalEntity: true,
		NoUpdate:    table == "budget_plan_decision",
	}
}
