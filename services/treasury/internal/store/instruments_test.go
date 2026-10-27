package store

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
	tevents "github.com/BenjaminSRussell/passage/services/treasury/internal/events"
)

func issue(t *testing.T, s *Postgres, ctx context.Context, accountID, key string) Check {
	t.Helper()
	c, err := s.IssueCheck(ctx, NewCheck{BankAccountID: accountID, Payee: "Northwind Traders", Amount: "1250.00", Currency: "EUR"}, key)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return c
}

// FR-36.6 and AC-36.5: numbers are gap-free per account, a reprint voids the
// old number and prints the next one with the same payee and amount, and every
// void writes check.voided to the outbox in the same transaction.
func TestChecks_GapFreeReprintVoidAndOutbox(t *testing.T) {
	s, admin := newStore(t)
	ctx := ctxFor(1, "")
	acct := activeAccount(t, s, ctx, ids.New())

	first := issue(t, s, ctx, acct.ID, "chk-a")
	if first.CheckNumber != 1 || first.Status != string(domain.CheckCreated) || first.LegalEntityID != acct.LegalEntityID {
		t.Fatalf("first check = %+v", first)
	}
	if replay := issue(t, s, ctx, acct.ID, "chk-a"); replay.ID != first.ID {
		t.Fatalf("replay issued %d", replay.CheckNumber)
	}
	if _, err := s.IssueCheck(ctx, NewCheck{BankAccountID: acct.ID, Payee: "Other", Amount: "1.00", Currency: "EUR"}, "chk-a"); problemCode(t, err) != CodeIdempotencyReuse {
		t.Fatalf("key reuse with another body: %v", err)
	}
	// A refused issue rolls back and does not consume a number.
	if _, err := s.IssueCheck(ctx, NewCheck{BankAccountID: acct.ID, Payee: "X", Amount: "1.00", Currency: "USD"}, ""); problemCode(t, err) != domain.CodeCheckCurrency {
		t.Fatalf("currency mismatch: %v", err)
	}
	second := issue(t, s, ctx, acct.ID, "chk-b")
	if second.CheckNumber != 2 {
		t.Fatalf("second number = %d, want 2", second.CheckNumber)
	}

	printed, err := s.PrintCheck(ctx, first.ID, first.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrintCheck(ctx, first.ID, first.Version); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	fresh, err := s.ReprintCheck(ctx, first.ID, printed.Version, "printer jam")
	if err != nil {
		t.Fatalf("reprint: %v", err)
	}
	if fresh.CheckNumber != 3 || fresh.Status != string(domain.CheckPrinted) || fresh.Payee != first.Payee ||
		!decimal.RequireFromString(fresh.Amount).Equal(decimal.RequireFromString("1250")) || fresh.ReplacesID == nil || *fresh.ReplacesID != first.ID {
		t.Fatalf("reprinted check = %+v", fresh)
	}
	old, err := s.GetCheck(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != string(domain.CheckVoided) || old.ReplacedByID == nil || *old.ReplacedByID != fresh.ID || old.VoidReason == nil || *old.VoidReason != "printer jam" {
		t.Fatalf("old check = %+v", old)
	}

	// A sent check needs a positive pay void record (BR-36.5).
	p2, err := s.PrintCheck(ctx, second.ID, second.Version)
	if err != nil {
		t.Fatal(err)
	}
	sent, err := s.SendCheck(ctx, second.ID, p2.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.VoidCheck(ctx, second.ID, sent.Version, "lost in the post"); err != nil {
		t.Fatalf("void sent: %v", err)
	}

	rows, err := admin.Query(context.Background(), `select aggregate_id, type, payload from outbox where aggregate_type = 'check' order by created_at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]tevents.CheckVoidedData{}
	for rows.Next() {
		var agg, typ string
		var payload []byte
		if err := rows.Scan(&agg, &typ, &payload); err != nil {
			t.Fatal(err)
		}
		if typ != tevents.CheckVoided {
			t.Fatalf("event type %s", typ)
		}
		var env struct {
			Data tevents.CheckVoidedData `json:"data"`
		}
		if err := json.Unmarshal(payload, &env); err != nil {
			t.Fatal(err)
		}
		got[agg] = env.Data
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("check.voided events = %v, want one for each voided check", got)
	}
	if e := got[first.ID]; e.Number != 1 || e.PositivePayVoid || e.Reason != "printer jam" || e.BankAccountID != acct.ID {
		t.Fatalf("reprint void event = %+v", e)
	}
	if e := got[second.ID]; e.Number != 2 || !e.PositivePayVoid {
		t.Fatalf("sent void event = %+v, want positivePayVoid", e)
	}

	list, err := s.ListChecks(ctx, acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].CheckNumber != 3 || list[2].CheckNumber != 1 {
		t.Fatalf("register = %+v", list)
	}
}

// BR-36.5: a cleared check cannot be voided, and the failed void writes no event.
func TestChecks_ClearedCannotBeVoided(t *testing.T) {
	s, admin := newStore(t)
	ctx := ctxFor(1, "")
	acct := activeAccount(t, s, ctx, ids.New())
	c := issue(t, s, ctx, acct.ID, "")
	var err error
	for _, step := range []func(context.Context, string, int) (Check, error){s.PrintCheck, s.SendCheck, s.ClearCheck} {
		if c, err = step(ctx, c.ID, c.Version); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.VoidCheck(ctx, c.ID, c.Version, "duplicate"); problemCode(t, err) != domain.CodeCheckCleared {
		t.Fatalf("void cleared: %v", err)
	}
	if _, err := s.ReprintCheck(ctx, c.ID, c.Version, "duplicate"); problemCode(t, err) != domain.CodeCheckCleared {
		t.Fatalf("reprint cleared: %v", err)
	}
	if n := count(t, admin, `select count(*) from outbox where aggregate_type = 'check'`); n != 0 {
		t.Fatalf("%d check events after refused voids", n)
	}
	if n := count(t, admin, `select count(*) from bank_check`); n != 1 {
		t.Fatalf("a refused reprint left %d checks", n)
	}
}

// Concurrent issues on one account get distinct, consecutive numbers.
func TestChecks_ConcurrentIssuesStayGapFree(t *testing.T) {
	s, _ := newStore(t)
	ctx := ctxFor(1, "")
	acct := activeAccount(t, s, ctx, ids.New())
	const n = 12
	numbers := make([]int, n)
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := s.IssueCheck(ctx, NewCheck{BankAccountID: acct.ID, Payee: "P", Amount: "1.00", Currency: "EUR"}, ids.New())
			if err != nil {
				errs <- err
				return
			}
			numbers[i] = int(c.CheckNumber)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	sort.Ints(numbers)
	for i, got := range numbers {
		if got != i+1 {
			t.Fatalf("numbers = %v, want 1..%d", numbers, n)
		}
	}
}

func TestChecks_RefusedOnInactiveAccount(t *testing.T) {
	s, _ := newStore(t)
	ctx := ctxFor(1, "")
	acct, err := s.CreateBankAccount(ctx, newAccount(ids.New()), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.IssueCheck(ctx, NewCheck{BankAccountID: acct.ID, Payee: "P", Amount: "1.00", Currency: "EUR"}, ""); problemCode(t, err) != CodeAccountInactive {
		t.Fatalf("draft account: %v", err)
	}
	if _, err := s.IssueCheck(ctx, NewCheck{BankAccountID: ids.New(), Payee: "P", Amount: "1.00", Currency: "EUR"}, ""); problemCode(t, err) != CodeAccountNotFound {
		t.Fatalf("unknown account: %v", err)
	}
}

func newFacility(t *testing.T, s *Postgres, ctx context.Context, le, limit string) Facility {
	t.Helper()
	f, err := s.CreateFacility(ctx, NewFacility{LegalEntityID: le, Code: "FAC-01", BankName: "Deutsche Bank", Currency: "EUR", Limit: limit, ExpiryDate: "2030-12-31"}, ids.New())
	if err != nil {
		t.Fatalf("facility: %v", err)
	}
	return f
}

func lc(facilityID, ref, amount string) NewInstrument {
	return NewInstrument{FacilityID: facilityID, Kind: domain.InstrumentLCImport, Reference: ref, Beneficiary: "Supplier GmbH",
		Currency: "EUR", Amount: amount, IssueDate: "2026-10-27", ExpiryDate: "2027-04-30"}
}

// AC-36.6: a facility of EUR 1,000,000 with EUR 950,000 utilized blocks an LC
// of EUR 100,000 without the override; with it the LC is booked and flagged,
// and closing an instrument releases its amount.
func TestFacility_LimitOverrideAndUtilization(t *testing.T) {
	s, _ := newStore(t)
	ctx := ctxFor(1, "")
	f := newFacility(t, s, ctx, ids.New(), "1000000.00")
	if _, err := s.CreateFacility(ctx, NewFacility{LegalEntityID: f.LegalEntityID, Code: "FAC-01", BankName: "Other", Currency: "EUR", Limit: "1.00", ExpiryDate: "2030-12-31"}, ""); problemCode(t, err) != CodeFacilityDuplicate {
		t.Fatalf("duplicate code: %v", err)
	}
	base, err := s.CreateInstrument(ctx, lc(f.ID, "LC-1", "950000.00"), false, "lc-1")
	if err != nil {
		t.Fatal(err)
	}
	if base.LimitOverride || base.Status != "open" || base.LegalEntityID != f.LegalEntityID {
		t.Fatalf("instrument = %+v", base)
	}
	if _, err := s.CreateInstrument(ctx, lc(f.ID, "LC-2", "100000.00"), false, "lc-2"); problemCode(t, err) != domain.CodeFacilityLimit {
		t.Fatalf("breach without override: %v", err)
	}
	over, err := s.CreateInstrument(ctx, lc(f.ID, "LC-2", "100000.00"), true, "lc-2")
	if err != nil {
		t.Fatalf("breach with override (the refused attempt must not hold the key): %v", err)
	}
	if !over.LimitOverride {
		t.Fatalf("override instrument = %+v", over)
	}
	got, err := s.GetFacility(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Utilized != "1050000.00" || got.Headroom != "0.00" || got.Limit != "1000000.00" {
		t.Fatalf("facility = %+v", got)
	}
	if _, err := s.CloseInstrument(ctx, base.ID, base.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseInstrument(ctx, base.ID, base.Version+1); problemCode(t, err) != CodeInstrumentState {
		t.Fatalf("close twice: %v", err)
	}
	list, err := s.ListFacilities(ctx, f.LegalEntityID)
	if err != nil || len(list) != 1 || list[0].Utilized != "100000.00" || list[0].Headroom != "900000.00" {
		t.Fatalf("after close: %+v, %v", list, err)
	}
	inst, err := s.ListInstruments(ctx, f.ID)
	if err != nil || len(inst) != 2 {
		t.Fatalf("instruments = %+v, %v", inst, err)
	}
	if _, err := s.CreateInstrument(ctx, NewInstrument{FacilityID: f.ID, Kind: domain.InstrumentGuarantee, Reference: "G", Beneficiary: "B",
		Currency: "USD", Amount: "1.00", IssueDate: "2026-10-27", ExpiryDate: "2026-12-31"}, false, ""); problemCode(t, err) != CodeInstrumentCurrency {
		t.Fatalf("currency mismatch: %v", err)
	}
	if _, err := s.CreateInstrument(ctx, NewInstrument{FacilityID: f.ID, Kind: domain.InstrumentGuarantee, Reference: "G", Beneficiary: "B",
		Currency: "EUR", Amount: "1.00", IssueDate: "2031-01-02", ExpiryDate: "2031-12-31"}, false, ""); problemCode(t, err) != CodeFacilityState {
		t.Fatalf("instrument after facility expiry: %v", err)
	}
	// Amounts are shown in the facility currency's minor unit (none for JPY).
	yen, err := s.CreateFacility(ctx, NewFacility{LegalEntityID: f.LegalEntityID, Code: "FAC-JPY", BankName: "MUFG", Currency: "JPY", Limit: "150000000", ExpiryDate: "2030-12-31"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if yen.Limit != "150000000" || yen.Utilized != "0" || yen.Headroom != "150000000" {
		t.Fatalf("JPY facility = %+v", yen)
	}
}

// The facility row lock serializes concurrent instruments: of two LCs that
// each fit alone but not together, exactly one is booked.
func TestFacility_ConcurrentInstrumentsRespectTheLimit(t *testing.T) {
	s, _ := newStore(t)
	ctx := ctxFor(1, "")
	f := newFacility(t, s, ctx, ids.New(), "1000000.00")
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, ref := range []string{"LC-A", "LC-B"} {
		wg.Add(1)
		go func(ref string) {
			defer wg.Done()
			_, err := s.CreateInstrument(ctx, lc(f.ID, ref, "600000.00"), false, ids.New())
			results <- err
		}(ref)
	}
	wg.Wait()
	close(results)
	booked, refused := 0, 0
	for err := range results {
		if err == nil {
			booked++
		} else if problemCode(t, err) == domain.CodeFacilityLimit {
			refused++
		}
	}
	if booked != 1 || refused != 1 {
		t.Fatalf("booked %d, refused %d; want 1 and 1", booked, refused)
	}
}

// FR-36.19 / AC-36.15 worked example: EUR 1,000,000 bullet at 3.00% ACT/360
// over a 30-day period accrues 2,500.00; the schedule persists and a repeated
// key replays the same loan.
func TestLoan_ScheduleStoredAndReplayed(t *testing.T) {
	s, admin := newStore(t)
	ctx := ctxFor(1, "")
	le := ids.New()
	start, _ := time.Parse("2006-01-02", "2026-06-01")
	maturity, _ := time.Parse("2006-01-02", "2026-09-01")
	schedule, err := domain.BuildSchedule(domain.LoanTerms{Principal: decimal.NewFromInt(1000000), Currency: "EUR", Start: start, Maturity: maturity,
		ScheduleType: domain.ScheduleBullet, Rate: decimal.RequireFromString("0.03"), DayCount: domain.DayCountACT360, PeriodMonths: 1})
	if err != nil {
		t.Fatal(err)
	}
	in := NewLoan{LegalEntityID: le, Role: "lender", CounterpartyName: "Passage Holdings BV", Principal: "1000000", Currency: "EUR",
		StartDate: "2026-06-01", MaturityDate: "2026-09-01", ScheduleType: "Bullet", FixedRate: "0.03", DayCount: "ACT/360", PeriodMonths: 1, Schedule: schedule}
	loan, err := s.CreateLoan(ctx, in, "loan-1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(loan.Schedule) != 3 || loan.Status != "active" {
		t.Fatalf("loan = %+v", loan)
	}
	june := loan.Schedule[0]
	if june.Days != 30 || june.Interest.StringFixed(2) != "2500.00" || !june.PrincipalRepayment.IsZero() {
		t.Fatalf("June = %+v", june)
	}
	if last := loan.Schedule[2]; !last.PrincipalRepayment.Equal(decimal.NewFromInt(1000000)) || !last.ClosingPrincipal.IsZero() {
		t.Fatalf("final period = %+v", last)
	}
	replay, err := s.CreateLoan(ctx, in, "loan-1")
	if err != nil || replay.ID != loan.ID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if n := count(t, admin, `select count(*) from treasury_loan_schedule_line where loan_id = $1`, loan.ID); n != 3 {
		t.Fatalf("stored %d schedule lines, want 3", n)
	}
	got, err := s.GetLoan(ctx, loan.ID)
	if err != nil || len(got.Schedule) != 3 || !got.Schedule[1].Interest.Equal(loan.Schedule[1].Interest) {
		t.Fatalf("get = %+v, %v", got, err)
	}
	list, err := s.ListLoans(ctx, le)
	if err != nil || len(list) != 1 || list[0].Schedule != nil {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if _, err := s.GetLoan(ctx, ids.New()); problemCode(t, err) != CodeLoanNotFound {
		t.Fatalf("unknown loan: %v", err)
	}
}

// Checks, facilities, instruments and loans are invisible to another tenant
// and to a caller scoped to another legal entity.
func TestRowLevelSecurity_ChecksFacilitiesLoans(t *testing.T) {
	s, _ := newStore(t)
	mine, le := ctxFor(1, ""), ids.New()
	acct := activeAccount(t, s, mine, le)
	c := issue(t, s, mine, acct.ID, "")
	f := newFacility(t, s, mine, le, "1000.00")
	inst, err := s.CreateInstrument(mine, lc(f.ID, "LC-1", "10.00"), false, "")
	if err != nil {
		t.Fatal(err)
	}
	loan, err := s.CreateLoan(mine, NewLoan{LegalEntityID: le, Role: "borrower", CounterpartyName: "Bank", Principal: "1000", Currency: "EUR",
		StartDate: "2026-01-01", MaturityDate: "2026-02-01", ScheduleType: "Bullet", FixedRate: "0.01", DayCount: "ACT/360", PeriodMonths: 1}, "")
	if err != nil {
		t.Fatal(err)
	}
	for name, ctx := range map[string]context.Context{"tenant 2": ctxFor(2, ""), "another legal entity": ctxFor(1, ids.New())} {
		if _, err := s.GetCheck(ctx, c.ID); problemCode(t, err) != CodeCheckNotFound {
			t.Fatalf("%s read the check: %v", name, err)
		}
		if _, err := s.GetFacility(ctx, f.ID); problemCode(t, err) != CodeFacilityNotFound {
			t.Fatalf("%s read the facility: %v", name, err)
		}
		if _, err := s.CreateInstrument(ctx, lc(f.ID, "LC-X", "1.00"), false, ""); problemCode(t, err) != CodeFacilityNotFound {
			t.Fatalf("%s booked on the facility: %v", name, err)
		}
		if _, err := s.CloseInstrument(ctx, inst.ID, inst.Version); problemCode(t, err) != CodeInstrumentNotFound {
			t.Fatalf("%s closed the instrument: %v", name, err)
		}
		if _, err := s.GetLoan(ctx, loan.ID); problemCode(t, err) != CodeLoanNotFound {
			t.Fatalf("%s read the loan: %v", name, err)
		}
		if _, err := s.IssueCheck(ctx, NewCheck{BankAccountID: acct.ID, Payee: "P", Amount: "1.00", Currency: "EUR"}, ""); problemCode(t, err) != CodeAccountNotFound {
			t.Fatalf("%s issued a check on the account: %v", name, err)
		}
	}
}
