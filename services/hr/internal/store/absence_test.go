package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
)

// usPTO15 is the BR-42.3 example plan: 1.25 days a month on the last day,
// prorated by days, max 22.50, carry-over max 5.00 expiring after 3 months.
func usPTO15(code string) AbsencePlan {
	return AbsencePlan{
		Code: code, Unit: "DAYS", AccrualFrequency: "MONTHLY", AccrualRate: "1.25", AccrualDay: "LAST_DAY",
		Proration: "DAYS_IN_PERIOD", MaxBalance: "22.50", CarryOverMax: "5.00", CarryOverExpiryMonths: 3,
		PlanYearStart: "01-01",
	}
}

func date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func at(s *Postgres, t time.Time) { s.now = func() time.Time { return t } }

func mustPlan(t *testing.T, s *Postgres, ctx context.Context, plan AbsencePlan) AbsencePlan {
	t.Helper()
	out, _, err := s.CreateAbsencePlan(ctx, plan, "")
	if err != nil {
		t.Fatalf("create plan %s: %v", plan.Code, err)
	}
	return out
}

func mustAccrueMonth(t *testing.T, s *Postgres, ctx context.Context, planID string, y int, m time.Month) AccrualRun {
	t.Helper()
	from := date(y, m, 1)
	run, err := s.RunAccrual(ctx, planID, from, from.AddDate(0, 1, -1))
	if err != nil {
		t.Fatalf("accrue %d-%02d: %v", y, m, err)
	}
	return run
}

func balanceOn(t *testing.T, s *Postgres, ctx context.Context, workerID string, asOf time.Time) BalanceView {
	t.Helper()
	b, err := s.Balances(ctx, workerID, asOf, false)
	if err != nil || len(b) != 1 {
		t.Fatalf("balances as of %s = %+v err=%v, want one plan", asOf.Format(absDateLayout), b, err)
	}
	return b[0]
}

func request(t *testing.T, s *Postgres, ctx context.Context, workerID, planID string, from, to time.Time, qty string) (AbsenceRequest, []domain.RuleWarning) {
	t.Helper()
	r, warnings, _, err := s.CreateAbsenceRequest(ctx, RequestInput{
		WorkerID: workerID, PlanID: planID, From: from, To: to, Quantity: decimal.RequireFromString(qty),
	}, "", false)
	if err != nil {
		t.Fatalf("request %s..%s for %s: %v", from.Format(absDateLayout), to.Format(absDateLayout), qty, err)
	}
	return r, warnings
}

// TestUSPTO15AccrualYearEndAndCarryOver is AC-42.3 through the database: hire
// 2026-03-16, March accrues 0.65, 11.90 by year end, 4.00 used leaves 7.90,
// the year end carries 5.00 and expires 2.90, January 2027 brings the total to
// 6.25, and the unused carry-over expires after 2027-03-31.
func TestUSPTO15AccrualYearEndAndCarryOver(t *testing.T) {
	s, ctx := newStore(t)
	at(s, date(2027, time.January, 1))
	plan := mustPlan(t, s, ctx, usPTO15("US-PTO-15"))
	w := mustWorker(t, s, ctx, "pto")
	mustEmployment(t, s, ctx, w.ID, ids.New(), "PRIMARY", day(time.March, 16))

	march := mustAccrueMonth(t, s, ctx, plan.ID, 2026, time.March)
	if march.Credited != 1 || march.Accrued != "0.65" || march.PlanYearStart != "2026-01-01" {
		t.Fatalf("March run = %+v, want 1 worker credited 0.65", march)
	}
	for m := time.April; m <= time.December; m++ {
		mustAccrueMonth(t, s, ctx, plan.ID, 2026, m)
	}
	if got := balanceOn(t, s, ctx, w.ID, day(time.December, 31)); got.Current.Accrued != "11.90" {
		t.Fatalf("accrued at 2026-12-31 = %s, want 11.90", got.Current.Accrued)
	}

	req, _ := request(t, s, ctx, w.ID, plan.ID, day(time.June, 1), day(time.June, 4), "4.00")
	if _, _, err := s.DecideAbsenceRequest(ctx, req.ID, domain.DecisionApprove); err != nil {
		t.Fatalf("approve: %v", err)
	}
	late, _ := request(t, s, ctx, w.ID, plan.ID, day(time.December, 28), day(time.December, 28), "1.00")
	if got := balanceOn(t, s, ctx, w.ID, day(time.December, 31)); got.Total != "7.90" || got.Current.Used != "4.00" {
		t.Fatalf("balance at 2026-12-31 = %+v, want 7.90 with 4.00 used", got)
	}

	_, err := s.RunAccrual(ctx, plan.ID, date(2027, time.January, 1), date(2027, time.January, 31))
	requireCode(t, err, CodeNotDue)
	at(s, date(2027, time.February, 1))
	_, err = s.RunAccrual(ctx, plan.ID, date(2027, time.January, 1), date(2027, time.January, 31))
	requireCode(t, err, CodeYearOpen)

	closed, err := s.CloseYear(ctx, plan.ID, day(time.January, 1))
	if err != nil || closed.Workers != 1 || closed.Carried != "5.00" || closed.Expired != "2.90" {
		t.Fatalf("year end = %+v err=%v, want 5.00 carried and 2.90 expired", closed, err)
	}
	again, err := s.CloseYear(ctx, plan.ID, day(time.January, 1))
	if err != nil || again.Workers != 0 || again.AlreadyClosed != 1 {
		t.Fatalf("second year end must close nobody: %+v err=%v", again, err)
	}
	if got := balanceOn(t, s, ctx, w.ID, day(time.December, 31)); got.Total != "5.00" || got.Current.Expired != "2.90" {
		t.Fatalf("closed 2026 = %+v, want 5.00 left (handed on) and 2.90 expired", got)
	}
	// The closed year's 5.00 now lives in 2027; spending or crediting 2026
	// again would count it twice.
	_, _, err = s.DecideAbsenceRequest(ctx, late.ID, domain.DecisionApprove)
	requireCode(t, err, CodeYearClosed)
	_, _, _, err = s.CreateAbsenceRequest(ctx, RequestInput{WorkerID: w.ID, PlanID: plan.ID, From: day(time.December, 29),
		To: day(time.December, 29), Quantity: decimal.RequireFromString("1.00")}, "", false)
	requireCode(t, err, CodeYearClosed)
	_, err = s.RunAccrual(ctx, plan.ID, day(time.December, 1), day(time.December, 31))
	requireCode(t, err, CodeYearClosed)

	mustAccrueMonth(t, s, ctx, plan.ID, 2027, time.January)
	jan := balanceOn(t, s, ctx, w.ID, date(2027, time.January, 31))
	if jan.CarryOver.Balance != "5.00" || jan.Current.Accrued != "1.25" || jan.Total != "6.25" {
		t.Fatalf("2027-01-31 = %+v, want carry-over 5.00 and total 6.25", jan)
	}

	_, err = s.ExpireCarryOver(ctx, plan.ID, date(2027, time.January, 1))
	requireCode(t, err, CodeNotDue)
	at(s, date(2027, time.April, 1))
	expired, err := s.ExpireCarryOver(ctx, plan.ID, date(2027, time.January, 1))
	if err != nil || expired.Workers != 1 || expired.Expired != "5.00" {
		t.Fatalf("carry-over expiry = %+v err=%v, want 5.00 expired", expired, err)
	}
	if got := balanceOn(t, s, ctx, w.ID, date(2027, time.April, 1)); got.CarryOver.Balance != "0.00" || got.Total != "1.25" {
		t.Fatalf("after expiry = %+v, want only the 1.25 accrued in January", got)
	}
	_, err = s.CloseYear(ctx, plan.ID, date(2026, time.March, 1))
	requireCode(t, err, domain.CodePeriod)

	detail, err := s.GetAbsencePlan(ctx, plan.ID)
	if err != nil || len(detail.Periods) != 11 || detail.Periods[0].PeriodStart != "2027-01-01" || len(detail.Closes) != 2 {
		t.Fatalf("plan detail = %d periods, %d closes, err=%v", len(detail.Periods), len(detail.Closes), err)
	}
}

// TestAccrualRunsAreKeyedPerWorkerAndPeriod covers the hr-accrue key (plan,
// period, worker): a rerun credits nobody twice but picks up a worker hired
// into the period after the first run; an overlapping second period, a period
// that is not one accrual period, and a period not yet due are refused.
func TestAccrualRunsAreKeyedPerWorkerAndPeriod(t *testing.T) {
	s, ctx := newStore(t)
	at(s, day(time.March, 1))
	plan := mustPlan(t, s, ctx, usPTO15("US-PTO-KEY"))
	first := mustWorker(t, s, ctx, "first")
	mustEmployment(t, s, ctx, first.ID, ids.New(), "PRIMARY", day(time.January, 1))

	if run := mustAccrueMonth(t, s, ctx, plan.ID, 2026, time.January); run.Credited != 1 {
		t.Fatalf("first run credited %d, want 1", run.Credited)
	}
	if run := mustAccrueMonth(t, s, ctx, plan.ID, 2026, time.January); run.Credited != 0 || run.AlreadyAccrued != 1 {
		t.Fatalf("rerun = %+v, want nobody credited", run)
	}
	late := mustWorker(t, s, ctx, "late")
	mustEmployment(t, s, ctx, late.ID, ids.New(), "PRIMARY", day(time.January, 1))
	if run := mustAccrueMonth(t, s, ctx, plan.ID, 2026, time.January); run.Credited != 1 || run.AlreadyAccrued != 1 {
		t.Fatalf("rerun after a late hire = %+v, want the new worker only", run)
	}
	if got := balanceOn(t, s, ctx, first.ID, day(time.January, 31)); got.Current.Accrued != "1.25" {
		t.Fatalf("first worker accrued %s, want 1.25 once", got.Current.Accrued)
	}

	_, err := s.RunAccrual(ctx, plan.ID, day(time.February, 1), day(time.February, 27))
	requireCode(t, err, domain.CodePeriod)
	_, err = s.RunAccrual(ctx, plan.ID, day(time.March, 1), day(time.March, 31))
	requireCode(t, err, CodeNotDue)

	bi := usPTO15("US-BI")
	bi.AccrualFrequency = "BIWEEKLY"
	biPlan := mustPlan(t, s, ctx, bi)
	if _, err := s.RunAccrual(ctx, biPlan.ID, day(time.January, 5), day(time.January, 18)); err != nil {
		t.Fatalf("biweekly run: %v", err)
	}
	_, err = s.RunAccrual(ctx, biPlan.ID, day(time.January, 12), day(time.January, 25))
	requireCode(t, err, CodeAccrualOverlap)

	capped := usPTO15("US-CAP")
	capped.MaxBalance = "1.00"
	capPlan := mustPlan(t, s, ctx, capped)
	run := mustAccrueMonth(t, s, ctx, capPlan.ID, 2026, time.January)
	if run.Accrued != "2.00" || run.Discarded != "0.50" {
		t.Fatalf("capped run = %+v, want 2.00 kept and 0.50 discarded", run)
	}
}

// TestAbsenceRequestRules covers FR-42.7, AC-42.4 and BR-42.5: the balance and
// span checks, the approved-overlap refusal (also when approving), the pending
// overlap warning, cancellation, idempotent replays and the approval event.
func TestAbsenceRequestRules(t *testing.T) {
	s, ctx := newStore(t)
	at(s, day(time.March, 1))
	p := usPTO15("US-PTO-2")
	p.AccrualRate = "1.00"
	plan := mustPlan(t, s, ctx, p)
	w := mustWorker(t, s, ctx, "rules")
	mustEmployment(t, s, ctx, w.ID, ids.New(), "PRIMARY", day(time.January, 1))
	mustAccrueMonth(t, s, ctx, plan.ID, 2026, time.January)
	mustAccrueMonth(t, s, ctx, plan.ID, 2026, time.February)

	in := RequestInput{WorkerID: w.ID, PlanID: plan.ID, From: day(time.March, 2), To: day(time.March, 4), Quantity: decimal.RequireFromString("3.00")}
	_, _, _, err := s.CreateAbsenceRequest(ctx, in, "", false)
	requireCode(t, err, "HCM-ABS-002")

	in.Quantity, in.To = decimal.RequireFromString("2.00"), day(time.March, 2)
	_, _, _, err = s.CreateAbsenceRequest(ctx, in, "", false)
	var pr *httpx.Problem
	if !errors.As(err, &pr) || pr.Status != 400 || pr.Errors["quantity"] == "" {
		t.Fatalf("2.00 days on a one-day span: err=%v, want 400 naming quantity", err)
	}

	_, _, _, err = s.CreateAbsenceRequest(ctx, RequestInput{WorkerID: w.ID, PlanID: plan.ID, From: day(time.December, 31),
		To: date(2027, time.January, 1), Quantity: decimal.RequireFromString("1.00")}, "", false)
	requireCode(t, err, domain.CodeYearSpan)

	a, _, inserted, err := s.CreateAbsenceRequest(ctx, RequestInput{WorkerID: w.ID, PlanID: plan.ID, From: day(time.March, 2),
		To: day(time.March, 3), Quantity: decimal.RequireFromString("1.00"), Reason: "dentist"}, "key-a", false)
	if err != nil || !inserted || a.Status != domain.RequestSubmitted || a.PlanCode != "US-PTO-2" || a.RequestedBy != "user-test" {
		t.Fatalf("request A = %+v inserted=%v err=%v", a, inserted, err)
	}
	replay, _, inserted, err := s.CreateAbsenceRequest(ctx, RequestInput{WorkerID: w.ID, PlanID: plan.ID, From: day(time.March, 2),
		To: day(time.March, 3), Quantity: decimal.RequireFromString("1.0")}, "key-a", false)
	if err != nil || inserted || replay.ID != a.ID {
		t.Fatalf("replay = %s inserted=%v err=%v, want the stored request", replay.ID, inserted, err)
	}
	_, _, _, err = s.CreateAbsenceRequest(ctx, RequestInput{WorkerID: w.ID, PlanID: plan.ID, From: day(time.March, 9),
		To: day(time.March, 9), Quantity: decimal.RequireFromString("1.00")}, "key-a", false)
	requireCode(t, err, CodeIdempotencyReuse)

	b, warnings := request(t, s, ctx, w.ID, plan.ID, day(time.March, 3), day(time.March, 3), "1.00")
	if len(warnings) != 1 || warnings[0].Code != domain.CodeOverlapPending {
		t.Fatalf("overlap with a submitted request: warnings = %+v, want HCM-ABS-003", warnings)
	}

	approved, changed, err := s.DecideAbsenceRequest(ctx, a.ID, domain.DecisionApprove)
	if err != nil || !changed || approved.Status != domain.RequestApproved || approved.DecidedBy != "user-test" || approved.DecidedAt == nil {
		t.Fatalf("approve A = %+v changed=%v err=%v", approved, changed, err)
	}
	if _, changed, err := s.DecideAbsenceRequest(ctx, a.ID, domain.DecisionApprove); err != nil || changed {
		t.Fatalf("replayed approval must be a no-op: changed=%v err=%v", changed, err)
	}
	if got := balanceOn(t, s, ctx, w.ID, day(time.March, 2)); got.Total != "1.00" {
		t.Fatalf("balance after approving 1.00 of 2.00 = %s, want 1.00", got.Total)
	}
	events := outboxFor(t, s, ctx, a.ID)
	if len(events) != 1 || events[0].Type != "dev.passage.hr.absence.approved.v1" ||
		events[0].Data["worker_id"] != w.ID || events[0].Data["date_from"] != "2026-03-02" || events[0].Data["date_to"] != "2026-03-03" ||
		events[0].Data["quantity"] != "1.00" || events[0].Data["unit"] != "DAYS" {
		t.Fatalf("approval event = %+v", events)
	}

	_, _, _, err = s.CreateAbsenceRequest(ctx, RequestInput{WorkerID: w.ID, PlanID: plan.ID, From: day(time.March, 3),
		To: day(time.March, 3), Quantity: decimal.RequireFromString("1.00")}, "", false)
	requireCode(t, err, domain.CodeOverlapApproved)
	_, _, err = s.DecideAbsenceRequest(ctx, b.ID, domain.DecisionApprove)
	requireCode(t, err, domain.CodeOverlapApproved)

	canceled, changed, err := s.CancelAbsenceRequest(ctx, b.ID, false)
	if err != nil || !changed || canceled.Status != domain.RequestCanceled {
		t.Fatalf("cancel B = %+v changed=%v err=%v", canceled, changed, err)
	}
	if _, changed, err := s.CancelAbsenceRequest(ctx, b.ID, false); err != nil || changed {
		t.Fatalf("replayed cancel must be a no-op: changed=%v err=%v", changed, err)
	}
	_, _, err = s.CancelAbsenceRequest(ctx, a.ID, false)
	requireCode(t, err, domain.CodeRequestState)
	_, _, err = s.DecideAbsenceRequest(ctx, b.ID, domain.DecisionReject)
	requireCode(t, err, domain.CodeRequestState)
	_, _, err = s.DecideAbsenceRequest(ctx, ids.New(), domain.DecisionApprove)
	requireCode(t, err, CodeAbsenceRequestNotFound)

	submitted, err := s.ListAbsenceRequests(ctx, RequestFilter{Status: domain.RequestApproved}, "", 10)
	if err != nil || len(submitted.Items) != 1 || submitted.Items[0].ID != a.ID || submitted.Items[0].PersonnelNumber != "WRK-rules" {
		t.Fatalf("approved list = %+v err=%v", submitted.Items, err)
	}
	all, err := s.ListAbsenceRequests(ctx, RequestFilter{WorkerID: w.ID}, "", 1)
	if err != nil || len(all.Items) != 1 || all.NextAfter == "" {
		t.Fatalf("first page = %+v err=%v, want one row and a cursor", all, err)
	}
	rest, err := s.ListAbsenceRequests(ctx, RequestFilter{WorkerID: w.ID}, all.NextAfter, 10)
	if err != nil || len(rest.Items) != 1 || rest.Items[0].ID == all.Items[0].ID {
		t.Fatalf("second page = %+v err=%v", rest.Items, err)
	}
}

// TestEmployeesSeeOnlyTheirOwnAbsence covers EssAbsence as own data and the
// 8.42.17 rule that an approver cannot approve their own request.
func TestEmployeesSeeOnlyTheirOwnAbsence(t *testing.T) {
	s, admin := newStore(t)
	at(s, day(time.March, 1))
	me := ids.New()
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: me, Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
	plan := mustPlan(t, s, admin, usPTO15("US-PTO-OWN"))
	mine, _, err := s.CreateWorker(admin, WorkerInput{Worker: Worker{PersonnelNumber: "WRK-ME", WorkerType: "EMPLOYEE",
		PartyPersonID: ids.New(), LegalName: "Me", UserID: &me}, NationalID: "123-45-6789"}, "")
	if err != nil {
		t.Fatal(err)
	}
	other := mustWorker(t, s, admin, "other")
	for _, w := range []string{mine.ID, other.ID} {
		mustEmployment(t, s, admin, w, ids.New(), "PRIMARY", day(time.January, 1))
	}
	mustAccrueMonth(t, s, admin, plan.ID, 2026, time.January)

	if _, err := s.Balances(ctx, mine.ID, day(time.January, 31), true); err != nil {
		t.Fatalf("own balances: %v", err)
	}
	_, err = s.Balances(ctx, other.ID, day(time.January, 31), true)
	requireCode(t, err, CodeNotOwnAbsence)
	_, _, _, err = s.CreateAbsenceRequest(ctx, RequestInput{WorkerID: other.ID, PlanID: plan.ID, From: day(time.March, 2),
		To: day(time.March, 2), Quantity: decimal.RequireFromString("1.00")}, "", true)
	requireCode(t, err, CodeNotOwnAbsence)

	own, _, _, err := s.CreateAbsenceRequest(ctx, RequestInput{WorkerID: mine.ID, PlanID: plan.ID, From: day(time.March, 2),
		To: day(time.March, 2), Quantity: decimal.RequireFromString("1.00")}, "", true)
	if err != nil {
		t.Fatalf("own request: %v", err)
	}
	request(t, s, admin, other.ID, plan.ID, day(time.March, 2), day(time.March, 2), "1.00")
	list, err := s.ListAbsenceRequests(ctx, RequestFilter{OwnUserID: me}, "", 10)
	if err != nil || len(list.Items) != 1 || list.Items[0].ID != own.ID {
		t.Fatalf("own list = %+v err=%v, want only my request", list.Items, err)
	}
	_, _, err = s.DecideAbsenceRequest(ctx, own.ID, domain.DecisionApprove)
	requireCode(t, err, domain.CodeSelfDecision)
	_, _, err = s.CancelAbsenceRequest(ctx, list.Items[0].ID, true)
	if err != nil {
		t.Fatalf("cancel own request: %v", err)
	}
}

// TestConcurrentApprovalsCannotOverspend: two requests that each fit the
// balance but not together are approved at once. The worker lock serialises
// the ledger writes, so exactly one succeeds and the other gets HCM-ABS-002.
func TestConcurrentApprovalsCannotOverspend(t *testing.T) {
	s, ctx := newStore(t)
	at(s, day(time.March, 1))
	plan := mustPlan(t, s, ctx, usPTO15("US-PTO-RACE"))
	w := mustWorker(t, s, ctx, "race")
	mustEmployment(t, s, ctx, w.ID, ids.New(), "PRIMARY", day(time.January, 1))
	mustAccrueMonth(t, s, ctx, plan.ID, 2026, time.January)
	one, _ := request(t, s, ctx, w.ID, plan.ID, day(time.March, 2), day(time.March, 2), "1.00")
	two, _ := request(t, s, ctx, w.ID, plan.ID, day(time.March, 9), day(time.March, 9), "1.00")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []string{one.ID, two.ID} {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			_, _, errs[i] = s.DecideAbsenceRequest(ctx, id, domain.DecisionApprove)
		}(i, id)
	}
	wg.Wait()
	failed := 0
	for _, err := range errs {
		if err != nil {
			requireCode(t, err, "HCM-ABS-002")
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("errors = %v, want exactly one approval refused", errs)
	}
	if got := balanceOn(t, s, ctx, w.ID, day(time.March, 1)); got.Total != "0.25" {
		t.Fatalf("balance = %s, want 0.25 after one 1.00 approval", got.Total)
	}
}

// TestAbsencePlanReplayAndDuplicate: a replayed key returns the plan, a key
// reused for another plan is refused, and a second plan with the same code is
// HCM-ABS-PLAN-DUPLICATE.
func TestAbsencePlanReplayAndDuplicate(t *testing.T) {
	s, ctx := newStore(t)
	plan, inserted, err := s.CreateAbsencePlan(ctx, usPTO15("US-PTO-REPLAY"), "plan-key")
	if err != nil || !inserted {
		t.Fatalf("create: inserted=%v err=%v", inserted, err)
	}
	again, inserted, err := s.CreateAbsencePlan(ctx, usPTO15("US-PTO-REPLAY"), "plan-key")
	if err != nil || inserted || again.ID != plan.ID {
		t.Fatalf("replay: id=%s inserted=%v err=%v", again.ID, inserted, err)
	}
	changed := usPTO15("US-PTO-REPLAY")
	changed.AccrualRate = "2.00"
	_, _, err = s.CreateAbsencePlan(ctx, changed, "plan-key")
	requireCode(t, err, CodeIdempotencyReuse)
	_, _, err = s.CreateAbsencePlan(ctx, usPTO15("US-PTO-REPLAY"), "other-key")
	requireCode(t, err, CodeAbsencePlanDuplicate)
	_, err = s.GetAbsencePlan(ctx, ids.New())
	requireCode(t, err, CodeAbsencePlanNotFound)

	mustPlan(t, s, ctx, usPTO15("AA-FIRST"))
	page, err := s.ListAbsencePlans(ctx, "", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].Code != "AA-FIRST" || page.NextAfter != "AA-FIRST" {
		t.Fatalf("plans page = %+v err=%v", page, err)
	}
}
