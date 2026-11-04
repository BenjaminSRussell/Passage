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
	scheme := domain.AccrualDefinition{LegalEntityID: entityID, LedgerID: ledgerID, Code: "RENT", DebitAccount: "6100", CreditAccount: "2100", Amount: "120.00", Frequency: "monthly", Occurrences: 2, Spread: domain.SpreadEqual}
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
}

func TestRegroupingRuleClaimsAreUniquePerRuleNotPerRuleSet(t *testing.T) {
	_, app := migrated(t)
	s := store.New(app)
	ctx := principal(1)
	save := func(key string, codes ...string) string {
		t.Helper()
		run, err := s.SaveRegroupingRun(ctx, domain.RegroupingRun{LegalEntityID: entityID, LedgerID: ledgerID, KeyDate: "2026-01-31", RuleCodes: codes, Mode: "post", Status: "proposed", CreatedBy: "u"}, key, key)
		if err != nil {
			t.Fatal(err)
		}
		return run.ID
	}
	first, reordered, overlapping, disjoint := save("k1", "A", "B"), save("k2", "B", "A"), save("k3", "A"), save("k4", "C")
	if err := s.ClaimRegroupingRules(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimRegroupingRules(ctx, first); err != nil {
		t.Fatalf("claiming again for the same run is idempotent: %v", err)
	}
	for name, id := range map[string]string{"reordered set": reordered, "overlapping subset": overlapping} {
		if code(s.ClaimRegroupingRules(ctx, id)) != domain.CodeRegroupingExists {
			t.Errorf("%s must conflict with the run holding rules A and B", name)
		}
	}
	if err := s.ClaimRegroupingRules(ctx, disjoint); err != nil {
		t.Fatalf("a different rule is free: %v", err)
	}
	if _, err := s.MarkRegroupingPosted(ctx, first, "je-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkRegroupingReversed(ctx, first, "je-2"); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimRegroupingRules(ctx, overlapping); err != nil {
		t.Fatalf("reversing a run frees its rules: %v", err)
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

func TestTransitionPeriod_PatchKeepsTheTemporaryReopenExpiry(t *testing.T) {
	admin, app := migrated(t)
	s := store.New(app)
	ctx := principal(1)
	registerJan(t, s, ctx)
	move := func(to domain.Status, group string) {
		t.Helper()
		if _, err := s.TransitionPeriod(ctx, domain.PeriodTransition{LegalEntityID: entityID, LedgerID: ledgerID, FiscalPeriodID: periodID, Module: domain.ModuleAll, To: to, AllowedGroupID: group, Actor: "mallory"}); err != nil {
			t.Fatalf("to %s: %v", to, err)
		}
	}
	move(domain.StatusClosed, "")
	req, err := s.RequestReopen(ctx, domain.StoredReopen{LegalEntityID: entityID, LedgerID: ledgerID, FiscalPeriodID: periodID, Module: domain.ModuleAll, Reason: "audit adjustment after close", RequestedBy: "alice", Required: 2, ExpiresDays: 3})
	if err != nil {
		t.Fatal(err)
	}
	for _, approver := range []string{"bob", "cara"} {
		if _, err := s.ApproveReopen(ctx, req.ID, approver); err != nil {
			t.Fatal(err)
		}
	}
	move(domain.StatusOnHold, "controllers")
	move(domain.StatusOpen, "")
	rows, err := s.PeriodMatrix(ctx, entityID, ledgerID)
	if err != nil || len(rows) == 0 || rows[0].ReopenExpiresAt == nil {
		t.Fatalf("a PATCH must keep the two-approver expiry: %+v %v", rows, err)
	}
	if _, err := admin.Exec(context.Background(), `update period_status set reopen_expires_at = now() - interval '1 hour' where fiscal_period_id = $1`, periodID); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ExpireReopened(context.Background()); err != nil || n != 1 {
		t.Fatalf("sweep closed %d, %v", n, err)
	}
	rows, _ = s.PeriodMatrix(ctx, entityID, ledgerID)
	if rows[0].Status != domain.StatusClosed || rows[0].ReopenExpiresAt != nil {
		t.Fatalf("expired period: %+v", rows[0])
	}
}

func TestReopenRequestAuditIdempotencyAndRejection(t *testing.T) {
	admin, app := migrated(t)
	s := store.New(app)
	ctx := principal(1)
	registerJan(t, s, ctx)
	if _, err := s.TransitionPeriod(ctx, domain.PeriodTransition{LegalEntityID: entityID, LedgerID: ledgerID, FiscalPeriodID: periodID, Module: domain.ModuleAll, To: domain.StatusClosed, Actor: "alice"}); err != nil {
		t.Fatal(err)
	}
	in := domain.StoredReopen{LegalEntityID: entityID, LedgerID: ledgerID, FiscalPeriodID: periodID, Module: domain.ModuleAll, Reason: "audit adjustment after close", RequestedBy: "alice", Required: 2, ExpiresDays: 3, IdempotencyKey: "reopen-1", RequestHash: "h1"}
	first, err := s.RequestReopen(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.RequestReopen(ctx, in)
	if err != nil || again.ID != first.ID {
		t.Fatalf("replay must return the stored request: %+v %v", again, err)
	}
	in.RequestHash = "other"
	if _, err := s.RequestReopen(ctx, in); code(err) != domain.CodeIdempotencyReuse {
		t.Fatalf("same key, different request: %v", err)
	}
	if _, err := s.RejectReopen(ctx, first.ID, "dana"); err != nil {
		t.Fatal(err)
	}
	var rejectedBy string
	if err := admin.QueryRow(context.Background(), `select rejected_by from period_reopen_request where id = $1`, first.ID).Scan(&rejectedBy); err != nil || rejectedBy != "dana" {
		t.Fatalf("rejecter %q %v", rejectedBy, err)
	}
	var changes int
	if err := admin.QueryRow(context.Background(), `select count(*) from period_status_change where actor in ('alice', 'dana') and reason like 'reopen %'`).Scan(&changes); err != nil || changes != 2 {
		t.Fatalf("request and rejection each leave an audit row, got %d %v", changes, err)
	}
	var events int
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where type = $1`, domain.EventPeriodStatusChanged).Scan(&events); err != nil || events < 3 {
		t.Fatalf("status-changed events = %d %v", events, err)
	}
}

func TestExpireReopened_ContinuesPastAFailingTenant(t *testing.T) {
	admin, app := migrated(t)
	s := store.New(app)
	other := "00000000-0000-0000-0000-0000000000c9"
	for n, entity := range map[int]string{1: entityID, 2: other} {
		if _, err := admin.Exec(context.Background(), `insert into period_status(tenant_id, legal_entity_id, ledger_id, fiscal_period_id, module, status, reopen_expires_at, changed_by)
			values($1,$2,$3,$4,'all','open', now() - interval '1 hour', 'test')`, testkit.TenantID(n), entity, ledgerID, periodID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := admin.Exec(context.Background(), `create function fail_first_tenant() returns trigger language plpgsql as $$ begin raise exception 'boom'; end $$;
		create trigger fail_first_tenant before update on period_status for each row when (old.tenant_id = '`+testkit.TenantID(1)+`') execute function fail_first_tenant()`); err != nil {
		t.Fatal(err)
	}
	n, err := s.ExpireReopened(context.Background())
	if err == nil || n != 1 {
		t.Fatalf("one tenant fails, the other still closes: closed %d, err %v", n, err)
	}
	var status string
	if err := admin.QueryRow(context.Background(), `select status from period_status where tenant_id = $1`, testkit.TenantID(2)).Scan(&status); err != nil || status != "closed" {
		t.Fatalf("second tenant status %q %v", status, err)
	}
}

func TestDueAccrualRunsBackOffAndParkWithoutBlockingOthers(t *testing.T) {
	_, app := migrated(t)
	s := store.New(app)
	ctx := principal(1)
	past := time.Now().Add(-time.Hour)
	save := func(key string) domain.AccrualRun {
		t.Helper()
		run, err := s.SaveAccrualRun(ctx, domain.AccrualRun{LegalEntityID: entityID, LedgerID: ledgerID, SchemeCode: "RENT", Amount: "10.00", Currency: "USD", DebitAccount: "6100", CreditAccount: "2100",
			StartDate: "2026-01-01", Status: "scheduled", Occurrences: []domain.AccrualOccurrence{}, EntryIDs: map[string]string{}, CreatedBy: "u", NextDue: &past}, key, key)
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	flaky, refused, healthy := save("a1"), save("a2"), save("a3")
	due, err := s.DueAccrualRuns(context.Background())
	if err != nil || len(due) != 3 || due[0].TenantID != testkit.TenantID(1) {
		t.Fatalf("due %+v %v", due, err)
	}
	if err := s.RecordAccrualFailure(ctx, flaky.ID, "ledger unavailable", false); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAccrualFailure(ctx, refused.ID, "ledger refused", true); err != nil {
		t.Fatal(err)
	}
	due, err = s.DueAccrualRuns(context.Background())
	if err != nil || len(due) != 1 || due[0].ID != healthy.ID {
		t.Fatalf("a failed run backs off and a parked run stops: %+v %v", due, err)
	}
	got, err := s.AccrualRun(ctx, refused.ID)
	if err != nil || got.Status != "parked" || got.LastError != "ledger refused" {
		t.Fatalf("parked run %+v %v", got, err)
	}
}

func TestChecklistEvidenceStartAndReassign(t *testing.T) {
	admin, app := migrated(t)
	s := store.New(app)
	ctx := principal(1)
	registerJan(t, s, ctx)
	tmplID := ids.New()
	if _, err := s.PutTemplate(ctx, domain.Template{ID: tmplID, Name: "month-end", Tasks: []domain.TemplateTask{
		{Key: "prep", Name: "Prep", Module: domain.ModuleGL, AssigneeRole: "controller", DependsOn: []string{}, EvidenceRequired: true},
	}}); err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateRun(ctx, tmplID, periodID, "alice", []string{entityID}, "run-1", "h1")
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := s.ListTasks(ctx, run.ID, "", "")
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks %+v %v", tasks, err)
	}
	task := tasks[0]
	if _, err := s.StartTask(ctx, task.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartTask(ctx, task.ID, "alice"); code(err) != domain.CodeInvalidTransition {
		t.Fatalf("starting a started task: %v", err)
	}
	if _, err := s.CompleteTask(ctx, task.ID, "alice", ""); code(err) != domain.CodeValidation {
		t.Fatalf("evidence is required: %v", err)
	}
	if _, err := s.ReassignTask(ctx, task.ID, "dana", "erin"); err != nil {
		t.Fatal(err)
	}
	var by string
	var events int
	if err := admin.QueryRow(context.Background(), `select reassigned_by from close_task where id = $1`, task.ID).Scan(&by); err != nil || by != "dana" {
		t.Fatalf("reassigned_by %q %v", by, err)
	}
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where type = $1`, domain.EventCloseTaskReassigned).Scan(&events); err != nil || events != 1 {
		t.Fatalf("reassign events = %d %v", events, err)
	}
	done, err := s.CompleteTask(ctx, task.ID, "alice", "s3://evidence/prep.pdf")
	if err != nil || done.Status != "completed" || done.EvidenceURI == "" {
		t.Fatalf("complete with evidence: %+v %v", done, err)
	}
}
