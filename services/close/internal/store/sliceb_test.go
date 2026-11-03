package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
	"github.com/BenjaminSRussell/passage/services/close/internal/store"
)

func registerJan(t *testing.T, s *store.Postgres, ctx context.Context) {
	t.Helper()
	if _, err := s.RegisterPeriod(ctx, domain.PeriodRegistration{
		LegalEntityID: entityID, LedgerID: ledgerID, FiscalPeriodID: periodID, Actor: "test",
		Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPeriodTransition_CloseRequiresReopenAndSweeperExpires(t *testing.T) {
	admin, app := migrated(t)
	s := store.New(app)
	ctx := principal(1)
	registerJan(t, s, ctx)
	closed, err := s.TransitionPeriod(ctx, domain.PeriodTransition{
		LegalEntityID: entityID, LedgerID: ledgerID, FiscalPeriodID: periodID, Module: domain.ModuleAll, To: domain.StatusClosed, Actor: "alice", Reason: "month end",
	})
	if err != nil || closed.Status != domain.StatusClosed {
		t.Fatalf("close: %+v %v", closed, err)
	}
	if _, err := s.TransitionPeriod(ctx, domain.PeriodTransition{
		LegalEntityID: entityID, LedgerID: ledgerID, FiscalPeriodID: periodID, Module: domain.ModuleAll, To: domain.StatusOpen, Actor: "alice",
	}); err == nil {
		t.Fatal("closed periods must not reopen through PATCH")
	}
	req, err := s.RequestReopen(ctx, domain.StoredReopen{
		LegalEntityID: entityID, LedgerID: ledgerID, FiscalPeriodID: periodID, Module: domain.ModuleAll,
		Reason: "audit adjustment after close", RequestedBy: "alice", Required: 2, ExpiresDays: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveReopen(ctx, req.ID, "alice"); err == nil {
		t.Fatal("requester cannot approve")
	}
	first, err := s.ApproveReopen(ctx, req.ID, "bob")
	if err != nil || first.Status != "pending" {
		t.Fatalf("first approval: %+v %v", first, err)
	}
	done, err := s.ApproveReopen(ctx, req.ID, "cara")
	if err != nil || done.Status != "approved" {
		t.Fatalf("second approval: %+v %v", done, err)
	}
	row, err := s.PeriodMatrix(ctx, entityID, ledgerID)
	if err != nil || len(row) == 0 || row[0].Status != domain.StatusOpen || row[0].ReopenExpiresAt == nil {
		t.Fatalf("reopened period: %+v %v", row, err)
	}
	if _, err := admin.Exec(context.Background(), `update period_status set reopen_expires_at = now() - interval '1 hour' where fiscal_period_id = $1`, periodID); err != nil {
		t.Fatal(err)
	}
	n, err := s.ExpireReopened(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("sweep closed %d, %v", n, err)
	}
	row, err = s.PeriodMatrix(ctx, entityID, ledgerID)
	if err != nil || row[0].Status != domain.StatusClosed {
		t.Fatalf("expired period: %+v %v", row, err)
	}
	var events int
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where type = $1`, domain.EventPeriodStatusChanged).Scan(&events); err != nil || events < 2 {
		t.Fatalf("status-changed events = %d %v", events, err)
	}
}

func TestPeriodMatrix_TenantIsolation(t *testing.T) {
	_, app := migrated(t)
	s := store.New(app)
	registerJan(t, s, principal(1))
	rows, err := s.PeriodMatrix(principal(2), entityID, ledgerID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("tenant 2 must see no periods, got %+v %v", rows, err)
	}
}

func TestChecklist_InstantiateAndComplete(t *testing.T) {
	_, app := migrated(t)
	s := store.New(app)
	ctx := principal(1)
	registerJan(t, s, ctx)
	tmplID := ids.New()
	if _, err := s.PutTemplate(ctx, domain.Template{ID: tmplID, Name: "month-end", Tasks: []domain.TemplateTask{
		{Key: "prep", Name: "Prep", Module: domain.ModuleGL, AssigneeRole: "controller", DependsOn: []string{}},
		{Key: "sign", Name: "Sign", Module: domain.ModuleGL, AssigneeRole: "controller", DependsOn: []string{"prep"}},
	}}); err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateRun(ctx, tmplID, periodID, "alice", []string{entityID}, "run-1", "h1")
	if err != nil || run.TaskCount != 2 {
		t.Fatalf("run %+v %v", run, err)
	}
	again, err := s.CreateRun(ctx, tmplID, periodID, "alice", []string{entityID}, "run-1", "h1")
	if err != nil || again.ID != run.ID {
		t.Fatalf("idempotent create: %+v %v", again, err)
	}
	if _, err := s.CreateRun(ctx, tmplID, periodID, "alice", []string{entityID}, "run-1", "other"); err == nil {
		t.Fatal("same key different body must conflict")
	}
	tasks, err := s.ListTasks(ctx, run.ID, "", "")
	if err != nil || len(tasks) != 2 {
		t.Fatalf("tasks %+v %v", tasks, err)
	}
	var prep, sign domain.BoardTask
	for _, tsk := range tasks {
		switch tsk.TaskKey {
		case "prep":
			prep = tsk
		case "sign":
			sign = tsk
		}
	}
	if _, err := s.CompleteTask(ctx, sign.ID, "alice", ""); code(err) != domain.CodeBlockedByDependency {
		t.Fatalf("sign before prep: %v", err)
	}
	if _, err := s.CompleteTask(ctx, prep.ID, "alice", ""); err != nil {
		t.Fatal(err)
	}
	if done, err := s.CompleteTask(ctx, sign.ID, "alice", ""); err != nil || done.Status != "completed" {
		t.Fatalf("sign: %+v %v", done, err)
	}
}

func TestAllocationAccrualRegrouping_TenantAndEntityIsolation(t *testing.T) {
	_, app := migrated(t)
	s := store.New(app)
	ctx := principal(1)
	rule := domain.AllocationRule{ID: ids.New(), LegalEntityID: entityID, LedgerID: ledgerID, Name: "rent", Method: "fixed_weight",
		SourceAccounts: []string{"6100"}, Destinations: []domain.AllocationDest{{Key: "A", Account: "6100", Weight: decimal.NewFromInt(1)}}, OffsetAccount: "6100"}
	if _, err := s.PutAllocationRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AllocationRule(principal(2), rule.ID); err == nil {
		t.Fatal("tenant 2 must not read tenant 1 allocation rules")
	}
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: otherEntityID})
	if _, err := s.AllocationRule(scoped, rule.ID); err == nil {
		t.Fatal("other legal entity must not read this rule")
	}
	scheme := domain.AccrualDefinition{LegalEntityID: entityID, LedgerID: ledgerID, Code: "RENT", DebitAccount: "6100", CreditAccount: "2100", Frequency: "monthly", Occurrences: 2, Spread: domain.SpreadEqual}
	if _, err := s.PutAccrualScheme(ctx, scheme); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AccrualScheme(principal(2), ledgerID, "RENT"); err == nil {
		t.Fatal("tenant 2 must not read tenant 1 accrual schemes")
	}
	rg := domain.StoredRegroupingRule{LegalEntityID: entityID, LedgerID: ledgerID, Code: "BANK", SortMethod: domain.SortAccountSign, SourceAccount: "1100", AdjustmentAccount: "2500", TargetAccount: "1100", NormalBalance: "debit", Sequence: 1}
	if _, err := s.PutRegroupingRule(ctx, rg); err != nil {
		t.Fatal(err)
	}
	run, err := s.SaveRegroupingRun(ctx, domain.RegroupingRun{LegalEntityID: entityID, LedgerID: ledgerID, KeyDate: "2026-01-31", RuleCodes: []string{"BANK"}, Mode: "simulate", Status: "proposed", Proposal: nil, CreatedBy: "u"}, "rg-1", "h1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegroupingRun(principal(2), run.ID); err == nil {
		t.Fatal("tenant 2 must not read tenant 1 regrouping runs")
	}
	posted, err := s.MarkRegroupingPosted(ctx, run.ID, "je-1")
	if err != nil || posted.Status != "posted" {
		t.Fatalf("post: %+v %v", posted, err)
	}
	if _, err := s.SaveRegroupingRun(ctx, domain.RegroupingRun{LegalEntityID: entityID, LedgerID: ledgerID, KeyDate: "2026-01-31", RuleCodes: []string{"BANK"}, Mode: "post", Status: "posted", CreatedBy: "u"}, "rg-2", "h2"); err == nil {
		t.Fatal("a second posted regrouping for the same key date and rules must conflict")
	} else {
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodeRegroupingExists {
			t.Fatalf("got %v, want CLOSE-0010", err)
		}
	}
}

func TestSliceBTables_AppRoleCannotSeeOtherTenant(t *testing.T) {
	admin, app := migrated(t)
	ruleID := ids.New()
	if _, err := admin.Exec(context.Background(), `insert into allocation_rule(tenant_id, id, legal_entity_id, ledger_id, name, method, source_filter, destinations, offset_account, updated_by)
		values($1,$2,$3,$4,'rent','fixed_weight','[]','[]','6100','u')`, testkit.TenantID(1), ruleID, entityID, ledgerID); err != nil {
		t.Fatal(err)
	}
	n := visibleRows(t, principal(1), app, "allocation_rule")
	if n != 1 {
		t.Fatalf("owner sees %d", n)
	}
	if got := visibleRows(t, principal(2), app, "allocation_rule"); got != 0 {
		t.Fatalf("other tenant sees %d", got)
	}
	err := dbx.WithTenantTx(principal(2), app, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `insert into allocation_rule(tenant_id, id, legal_entity_id, ledger_id, name, method, source_filter, destinations, offset_account, updated_by)
			values($1,$2,$3,$4,'x','fixed_weight','[]','[]','6100','u')`, testkit.TenantID(1), ids.New(), entityID, ledgerID)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "row-level security") {
		t.Fatalf("cross-tenant write: %v", err)
	}
}

func code(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}
