package store_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/expense/internal/domain"
	"github.com/BenjaminSRussell/passage/services/expense/internal/events"
	"github.com/BenjaminSRussell/passage/services/expense/internal/store"
	"github.com/BenjaminSRussell/passage/services/expense/migrations"
)

// setup migrates a fresh schema and returns a store whose principal (tenant n) is
// scoped to one legal entity, which it also returns.
func setup(t *testing.T, n int) (*store.Store, context.Context, string, *pgxpool.Pool) {
	t.Helper()
	pool := testkit.DB(t)
	le := ids.New()
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), LegalEntityID: le})
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return store.New(pool), ctx, le, pool
}

func problemOf(t *testing.T, err error) *httpx.Problem {
	t.Helper()
	var p *httpx.Problem
	if !errors.As(err, &p) {
		t.Fatalf("error = %v; want an httpx problem", err)
	}
	return p
}

func outboxTypes(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []string {
	t.Helper()
	p, _ := tenant.FromContext(ctx)
	types := []string{}
	if err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select type from outbox where tenant_id=$1 order by created_at`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var typ string
			if err := rows.Scan(&typ); err != nil {
				return err
			}
			types = append(types, typ)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return types
}

var (
	businessDay = time.Date(2025, 3, 3, 0, 0, 0, 0, time.UTC)
	commitDay   = businessDay.AddDate(0, 0, 1)
)

func newReport(t *testing.T, s *store.Store, ctx context.Context, le, key string) store.Report {
	t.Helper()
	rep, _, err := s.CreateReport(ctx, key, "h-"+key, store.NewReport{LegalEntityID: le, WorkerID: ids.New(), Purpose: "Client visit", Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func line(category, merchant string, amount int64) domain.Line {
	return domain.Line{ID: ids.New(), Category: category, TxnDate: businessDay, Amount: decimal.NewFromInt(amount), Merchant: merchant}
}

func TestPostgres_CreateReplayKeyReuseAndScope(t *testing.T) {
	s, ctx, le, _ := setup(t, 11)
	in := store.NewReport{LegalEntityID: le, WorkerID: ids.New(), Purpose: "Client visit", Currency: "USD"}

	first, replayed, err := s.CreateReport(ctx, "create-1", "h1", in)
	if err != nil || replayed || first.Status != "DRAFT" || first.Version != 1 || !first.Total.IsZero() {
		t.Fatalf("create = %+v replayed=%v err=%v; want a new empty DRAFT at version 1", first, replayed, err)
	}
	again, replayed, err := s.CreateReport(ctx, "create-1", "h1", in)
	if err != nil || !replayed || again.ID != first.ID {
		t.Fatalf("replay = %+v replayed=%v err=%v; want the original report", again, replayed, err)
	}
	_, _, err = s.CreateReport(ctx, "create-1", "other-hash", in)
	if p := problemOf(t, err); p.Code != store.CodeIdempotencyReuse || p.Status != http.StatusUnprocessableEntity {
		t.Fatalf("reused key = %s/%d; want %s/422", p.Code, p.Status, store.CodeIdempotencyReuse)
	}
	in.LegalEntityID = ids.New()
	_, _, err = s.CreateReport(ctx, "create-2", "h2", in)
	if p := problemOf(t, err); p.Code != store.CodeOutOfScope || p.Status != http.StatusForbidden {
		t.Fatalf("other legal entity = %s/%d; want %s/403", p.Code, p.Status, store.CodeOutOfScope)
	}
}

func TestPostgres_CreateRefusesCallerWithoutLegalEntityScope(t *testing.T) {
	s, _, le, _ := setup(t, 12)
	unscoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(12)})
	in := store.NewReport{LegalEntityID: le, WorkerID: ids.New(), Purpose: "Client visit", Currency: "USD"}
	_, _, err := s.CreateReport(unscoped, "create-noscope", "h-noscope", in)
	if p := problemOf(t, err); p.Code != store.CodeOutOfScope || p.Status != http.StatusForbidden {
		t.Fatalf("no legal-entity scope = %s/%d; want %s/403", p.Code, p.Status, store.CodeOutOfScope)
	}
}

// Lines are evaluated at commit across the report: a second 40.00 meal on the same
// day takes the day over 75.00, so both meal lines carry EXP-MEAL-075, and the
// stored totals follow the lines.
func TestPostgres_AddLineReevaluatesTheReportAndKeepsTotals(t *testing.T) {
	s, ctx, le, _ := setup(t, 12)
	rep := newReport(t, s, ctx, le, "create")
	lunch := line("MEALS", "Deli", 40)
	lunch.HasReceipt = true
	rep, replayed, err := s.AddLine(ctx, rep.ID, "line-1", "h1", lunch, commitDay)
	if err != nil || replayed || len(rep.Lines) != 1 || len(rep.Lines[0].Results) != 0 || rep.Version != 2 {
		t.Fatalf("first meal = %+v replayed=%v err=%v; want one clean line at version 2", rep, replayed, err)
	}
	dinner := line("MEALS", "Bistro", 40)
	dinner.HasReceipt = true
	dinner.PaidBy = domain.PaidByCompanyCard
	if rep, _, err = s.AddLine(ctx, rep.ID, "line-2", "h2", dinner, commitDay); err != nil {
		t.Fatal(err)
	}
	for _, l := range rep.Lines {
		if len(l.Results) != 1 || l.Results[0].RuleID != "EXP-MEAL-075" || l.Results[0].Satisfied {
			t.Errorf("line %d results = %+v; want an unsatisfied EXP-MEAL-075", l.LineNo, l.Results)
		}
	}
	if rep.Lines[0].LineNo != 1 || rep.Lines[1].LineNo != 2 {
		t.Errorf("line numbers = %d, %d; want 1, 2", rep.Lines[0].LineNo, rep.Lines[1].LineNo)
	}
	if !rep.Total.Equal(decimal.NewFromInt(80)) || !rep.Reimbursable.Equal(decimal.NewFromInt(40)) || !rep.CompanyPaid.Equal(decimal.NewFromInt(40)) {
		t.Errorf("totals = %s / %s / %s; want 80 = 40 reimbursable + 40 company-paid", rep.Total, rep.Reimbursable, rep.CompanyPaid)
	}

	replay, replayed, err := s.AddLine(ctx, rep.ID, "line-2", "h2", line("MEALS", "Other", 1), commitDay)
	if err != nil || !replayed || len(replay.Lines) != 2 {
		t.Fatalf("replayed line = %d lines replayed=%v err=%v; want the same two lines", len(replay.Lines), replayed, err)
	}
	_, _, err = s.AddLine(ctx, rep.ID, "line-2", "different", line("MEALS", "Other", 1), commitDay)
	if p := problemOf(t, err); p.Code != store.CodeIdempotencyReuse {
		t.Fatalf("reused line key = %s; want %s", p.Code, store.CodeIdempotencyReuse)
	}
	_, _, err = s.AddLine(ctx, rep.ID, "line-3", "h3", domain.Line{ID: lunch.ID, Category: "TAXI", TxnDate: businessDay, Amount: decimal.NewFromInt(5)}, commitDay)
	if p := problemOf(t, err); p.Code != store.CodeLineExists || p.Status != http.StatusConflict {
		t.Fatalf("repeated line id = %s/%d; want %s/409", p.Code, p.Status, store.CodeLineExists)
	}
	_, _, err = s.AddLine(ctx, ids.New(), "line-4", "h4", line("TAXI", "Cab", 5), commitDay)
	if p := problemOf(t, err); p.Code != store.CodeReportNotFound {
		t.Fatalf("unknown report = %s; want %s", p.Code, store.CodeReportNotFound)
	}
}

// A refused submit keeps the report DRAFT, commits one policy.violated.v1 per BLOCK,
// and replays from its key without a second event. Deleting the flagged line fixes
// the report and a new submit succeeds.
func TestPostgres_SubmitRefusalCommitsBlockEventAndReplays(t *testing.T) {
	s, ctx, le, pool := setup(t, 13)
	rep := newReport(t, s, ctx, le, "create")
	a := line("TRAVEL", "ACME", 30)
	b := line("TRAVEL", "acme", 30)
	b.HasReceipt = true
	var err error
	if rep, _, err = s.AddLine(ctx, rep.ID, "line-a", "ha", a, commitDay); err != nil {
		t.Fatal(err)
	}
	if rep, _, err = s.AddLine(ctx, rep.ID, "line-b", "hb", b, commitDay); err != nil {
		t.Fatal(err)
	}

	_, _, err = s.SubmitReport(ctx, rep.ID, "submit-1", "hs1", 0, commitDay)
	p := problemOf(t, err)
	if p.Status != http.StatusUnprocessableEntity || p.Code != domain.CodeReceipt || len(p.Errors) != 2 ||
		p.Errors[a.ID+"/EXP-RCPT-025"] == "" || p.Errors[b.ID+"/EXP-DUP-001"] == "" {
		t.Fatalf("refusal = %d %s %v; want 422 %s with the receipt on a and the duplicate on b", p.Status, p.Code, p.Errors, domain.CodeReceipt)
	}
	if got := outboxTypes(t, ctx, pool); len(got) != 1 || got[0] != events.PolicyViolated {
		t.Fatalf("outbox = %v; want one %s for the BLOCK duplicate only", got, events.PolicyViolated)
	}
	_, _, err = s.SubmitReport(ctx, rep.ID, "submit-1", "hs1", 0, commitDay)
	if again := problemOf(t, err); again.Code != p.Code || len(again.Errors) != 2 {
		t.Fatalf("replayed refusal = %s %v; want the stored refusal", again.Code, again.Errors)
	}
	if got := outboxTypes(t, ctx, pool); len(got) != 1 {
		t.Fatalf("outbox after replay = %v; a replay must not publish again", got)
	}
	still, err := s.GetReport(ctx, rep.ID)
	if err != nil || still.Status != "DRAFT" || still.SubmittedAt != nil {
		t.Fatalf("refused report = %+v, %v; want DRAFT", still, err)
	}

	_, err = s.DeleteLine(ctx, rep.ID, a.ID, still.Version+1, commitDay)
	if p := problemOf(t, err); p.Code != store.CodeVersionConflict || p.Status != http.StatusPreconditionFailed {
		t.Fatalf("stale delete = %s/%d; want %s/412", p.Code, p.Status, store.CodeVersionConflict)
	}
	fixed, err := s.DeleteLine(ctx, rep.ID, a.ID, still.Version, commitDay)
	if err != nil || len(fixed.Lines) != 1 || len(fixed.Lines[0].Results) != 0 || !fixed.Total.Equal(decimal.NewFromInt(30)) {
		t.Fatalf("after delete = %+v, %v; want line b alone, clean, total 30", fixed, err)
	}
	if _, err := s.DeleteLine(ctx, rep.ID, a.ID, 0, commitDay); problemOf(t, err).Code != store.CodeLineNotFound {
		t.Fatalf("deleting a deleted line must be %s", store.CodeLineNotFound)
	}
	submitted, _, err := s.SubmitReport(ctx, rep.ID, "submit-2", "hs2", fixed.Version, commitDay)
	if err != nil || submitted.Status != "SUBMITTED" {
		t.Fatalf("submit after fix = %+v, %v; want SUBMITTED", submitted, err)
	}
}

func TestPostgres_CleanSubmitFreezesTheReport(t *testing.T) {
	s, ctx, le, pool := setup(t, 14)
	rep := newReport(t, s, ctx, le, "create")
	if _, _, err := s.SubmitReport(ctx, rep.ID, "submit-empty", "he", 0, commitDay); problemOf(t, err).Code != store.CodeReportEmpty {
		t.Fatalf("empty submit must be %s", store.CodeReportEmpty)
	}
	taxi := line("TRAVEL", "Cab", 12)
	taxi.PaidBy = domain.PaidByCompanyCard
	rep, _, err := s.AddLine(ctx, rep.ID, "line-c", "hc", taxi, commitDay)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SubmitReport(ctx, rep.ID, "submit-stale", "hst", rep.Version-1, commitDay); problemOf(t, err).Code != store.CodeVersionConflict {
		t.Fatalf("stale If-Match must be %s", store.CodeVersionConflict)
	}
	submitted, replayed, err := s.SubmitReport(ctx, rep.ID, "submit-2", "hs", rep.Version, commitDay)
	if err != nil || replayed || submitted.Status != "SUBMITTED" || submitted.SubmittedAt == nil || submitted.Version != rep.Version+1 {
		t.Fatalf("submit = %+v replayed=%v err=%v; want SUBMITTED at the next version", submitted, replayed, err)
	}
	if got := outboxTypes(t, ctx, pool); len(got) != 0 {
		t.Fatalf("clean submit published %v; want nothing (report.approved is a later slice)", got)
	}
	again, replayed, err := s.SubmitReport(ctx, rep.ID, "submit-2", "hs", rep.Version, commitDay)
	if err != nil || !replayed || again.Status != "SUBMITTED" {
		t.Fatalf("replayed submit = %+v replayed=%v err=%v; want the submitted report", again, replayed, err)
	}
	if _, _, err := s.SubmitReport(ctx, rep.ID, "submit-2", "other", 0, commitDay); problemOf(t, err).Code != store.CodeIdempotencyReuse {
		t.Fatalf("submit key reused with another request must be %s", store.CodeIdempotencyReuse)
	}
	if _, _, err := s.SubmitReport(ctx, rep.ID, "submit-3", "hs3", 0, commitDay); problemOf(t, err).Code != store.CodeReportState {
		t.Fatalf("second submit must be %s", store.CodeReportState)
	}
	if _, _, err := s.AddLine(ctx, rep.ID, "line-d", "hd", line("TRAVEL", "Bus", 5), commitDay); problemOf(t, err).Code != store.CodeReportState {
		t.Fatalf("adding to a submitted report must be %s", store.CodeReportState)
	}
	if _, err := s.DeleteLine(ctx, rep.ID, taxi.ID, 0, commitDay); problemOf(t, err).Code != store.CodeReportState {
		t.Fatalf("deleting from a submitted report must be %s", store.CodeReportState)
	}
}

func TestPostgres_ListPagesFiltersAndIsTenantIsolated(t *testing.T) {
	s, ctx, le, _ := setup(t, 15)
	var made []store.Report
	for _, key := range []string{"r1", "r2", "r3"} {
		made = append(made, newReport(t, s, ctx, le, key))
	}
	page, next, err := s.ListReports(ctx, store.ReportFilter{}, "", 2)
	if err != nil || len(page) != 2 || next != page[1].ID || page[0].ID != made[0].ID {
		t.Fatalf("first page = %d next %q err %v; want 2 reports in id order and a cursor", len(page), next, err)
	}
	rest, next, err := s.ListReports(ctx, store.ReportFilter{}, next, 2)
	if err != nil || len(rest) != 1 || next != "" || rest[0].ID != made[2].ID {
		t.Fatalf("last page = %d next %q err %v; want the third report and no cursor", len(rest), next, err)
	}
	byWorker, _, err := s.ListReports(ctx, store.ReportFilter{WorkerID: made[1].WorkerID}, "", 10)
	if err != nil || len(byWorker) != 1 || byWorker[0].ID != made[1].ID {
		t.Fatalf("worker filter = %+v, %v; want report 2 only", byWorker, err)
	}
	submitted, _, err := s.ListReports(ctx, store.ReportFilter{Status: "SUBMITTED"}, "", 10)
	if err != nil || len(submitted) != 0 {
		t.Fatalf("status filter = %d, %v; want none submitted", len(submitted), err)
	}

	other := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(16), LegalEntityID: le})
	theirs, _, err := s.ListReports(other, store.ReportFilter{}, "", 10)
	if err != nil || len(theirs) != 0 {
		t.Fatalf("other tenant list = %d, %v; want 0", len(theirs), err)
	}
	if _, err := s.GetReport(other, made[0].ID); problemOf(t, err).Code != store.CodeReportNotFound {
		t.Fatalf("another tenant's report must be %s", store.CodeReportNotFound)
	}
}

// Row-level security, not only the store's own tenant predicates, hides another
// tenant's and another legal entity's reports and lines. Skipped when the test role
// bypasses RLS (a superuser).
func TestPostgres_RowLevelSecurityHidesOtherScopes(t *testing.T) {
	s, ctx, le, pool := setup(t, 17)
	var bypass bool
	if err := pool.QueryRow(ctx, `select rolsuper or rolbypassrls from pg_roles where rolname = current_user`).Scan(&bypass); err != nil {
		t.Fatal(err)
	}
	if bypass {
		t.Skip("test role bypasses row-level security")
	}
	rep := newReport(t, s, ctx, le, "create")
	if _, _, err := s.AddLine(ctx, rep.ID, "line", "h", line("TAXI", "Cab", 9), commitDay); err != nil {
		t.Fatal(err)
	}
	count := func(c context.Context) (reports, lines int) {
		t.Helper()
		if err := dbx.WithTenantTx(c, pool, func(tx pgx.Tx) error {
			return tx.QueryRow(c, `select (select count(*) from expense_report), (select count(*) from expense_line)`).Scan(&reports, &lines)
		}); err != nil {
			t.Fatal(err)
		}
		return reports, lines
	}
	if r, l := count(ctx); r != 1 || l != 1 {
		t.Fatalf("own scope sees %d reports, %d lines; want 1, 1", r, l)
	}
	otherTenant := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(18), LegalEntityID: le})
	if r, l := count(otherTenant); r != 0 || l != 0 {
		t.Fatalf("another tenant sees %d reports, %d lines; want none", r, l)
	}
	otherEntity := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(17), LegalEntityID: ids.New()})
	if r, l := count(otherEntity); r != 0 || l != 0 {
		t.Fatalf("another legal entity sees %d reports, %d lines; want none", r, l)
	}
}
