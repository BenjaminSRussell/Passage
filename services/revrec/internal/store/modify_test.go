package store

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/domain"
	rev "github.com/BenjaminSRussell/passage/services/revrec/internal/events"
)

// supportAndLicence is a 2,000.00 contract of 2027-01-01: a perpetual licence (milestone, SSP 800.00) and a 12-month support plan
// (RATABLE, SSP 1,200.00, 100.00 a month on the MONTHLY basis), both allocated at their SSP.
func supportAndLicence(t *testing.T, legalEntityID string) Contract {
	t.Helper()
	obs := []domain.Obligation{
		{ID: "lic", SSP: decimal.RequireFromString("800"), SSPSource: "EXPLICIT", Method: domain.MethodMilestone},
		{ID: "sup", SSP: decimal.RequireFromString("1200"), SSPSource: "EXPLICIT", Method: domain.MethodRatable},
	}
	allocated, err := domain.Allocate(money.Must("2000.00", "USD"), obs, domain.ResidualLargest)
	if err != nil {
		t.Fatal(err)
	}
	c := Contract{ID: ids.New(), LegalEntityID: legalEntityID, OrderID: ids.New(), CustomerID: ids.New(), Currency: "USD",
		TransactionPrice: decimal.RequireFromString("2000.00"), ResidualRule: domain.ResidualLargest, RatableBasis: domain.BasisMonthly,
		ContractDate: day(2027, time.January, 1), IdempotencyKey: ids.New(), RequestHash: "hash"}
	start, end := day(2027, time.January, 1), day(2027, time.December, 31)
	for i, o := range allocated {
		ob := Obligation{ID: ids.New(), ContractID: c.ID, LineNo: i + 1, ProductID: ids.New(), SSP: o.SSP, SSPSource: o.SSPSource, Allocated: o.Allocated, Method: o.Method}
		if o.Method == domain.MethodRatable {
			ob.TermStart, ob.TermEnd = &start, &end
		}
		lines, err := domain.ScheduleLines(o, "USD", domain.BasisMonthly, ob.TermStart, ob.TermEnd, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range lines {
			ob.Schedule = append(ob.Schedule, Schedule{ID: ids.New(), Seq: l.Seq, Date: l.Date, Amount: l.Amount, Status: domain.ScheduleScheduled})
		}
		c.Obligations = append(c.Obligations, ob)
	}
	return c
}

func recognizeThrough(t *testing.T, s *Postgres, ctx context.Context, le string, months int) {
	t.Helper()
	for m := 1; m <= months; m++ {
		end := day(2027, time.Month(m)+1, 1).AddDate(0, 0, -1)
		if _, _, err := s.RunRecognition(ctx, le, end); err != nil {
			t.Fatalf("run %s: %v", end.Format(dateLayout), err)
		}
	}
}

// The catch-up of worked example 3 through Postgres: price +1,000.00 on 2027-03-31 with the licence satisfied and three support months
// recognized. The licence catches up 400.00 at once, the support 150.00 on the next run, and the contract recognizes exactly its new
// price 3,000.00 by the end of the term, with the deferred revenue roll-forward closing at zero.
func TestModifyContract_CatchUpRecognizesTheCatchUpAndTheRollForwardTiesOut(t *testing.T) {
	s, pool := testStore(t)
	ctx := tenantCtx(1, "")
	le := ids.New()
	c, err := s.CreateContract(ctx, supportAndLicence(t, le))
	if err != nil {
		t.Fatal(err)
	}
	lic, sup := c.Obligations[0], c.Obligations[1]
	if _, err := s.SatisfyMilestone(ctx, lic.ID, day(2027, time.January, 15), 0); err != nil {
		t.Fatal(err)
	}
	recognizeThrough(t, s, ctx, le, 3)
	cur, _ := s.GetContract(ctx, c.ID)

	req := ModificationRequest{Type: domain.ModCatchUp, EffectiveDate: day(2027, time.March, 31), PriceChange: decimal.RequireFromString("1000.00"),
		Reason: "scope extension", ExpectedVersion: cur.Version}
	if _, _, err := s.ModifyContract(ctx, c.ID, ModificationRequest{Type: domain.ModCatchUp, EffectiveDate: day(2027, time.March, 31),
		PriceChange: decimal.RequireFromString("1000.00"), ExpectedVersion: cur.Version + 5}); problemCode(err) != CodeVersion {
		t.Fatalf("stale version: %v", err)
	}
	if _, _, err := s.ModifyContract(ctx, c.ID, ModificationRequest{Type: domain.ModCatchUp, EffectiveDate: day(2026, time.December, 31),
		PriceChange: decimal.RequireFromString("1000.00")}); problemCode(err) != CodeModificationBeforeContract {
		t.Fatalf("before the contract date: %v", err)
	}
	if _, _, err := s.ModifyContract(ctx, c.ID, ModificationRequest{Type: domain.ModCatchUp, EffectiveDate: day(2027, time.March, 31),
		PriceChange: decimal.RequireFromString("-1900.00")}); problemCode(err) != domain.CodeModBelowRecognized {
		t.Fatalf("a cut below recognized revenue: %v", err)
	}

	got, mod, err := s.ModifyContract(ctx, c.ID, req)
	if err != nil {
		t.Fatalf("modify: %v", err)
	}
	if !got.TransactionPrice.Equal(decimal.RequireFromString("3000.00")) || got.Version != cur.Version+1 || !mod.CatchUp.Equal(decimal.RequireFromString("550.00")) {
		t.Fatalf("contract %s v%d catch-up %s", got.TransactionPrice, got.Version, mod.CatchUp)
	}
	if _, _, err := s.ModifyContract(ctx, c.ID, req); problemCode(err) != CodeVersion {
		t.Fatalf("a retry of the same modification: %v, want %s", err, CodeVersion)
	}
	gl, gs := got.Obligations[0], got.Obligations[1]
	if !gl.Allocated.Equal(decimal.RequireFromString("1200.00")) || !gs.Allocated.Equal(decimal.RequireFromString("1800.00")) {
		t.Fatalf("allocation %s / %s", gl.Allocated, gs.Allocated)
	}
	if n := len(gl.Schedule); n != 2 || gl.Schedule[1].Status != domain.ScheduleRecognized || !gl.Schedule[1].Amount.Equal(decimal.RequireFromString("400.00")) {
		t.Fatalf("licence schedule = %+v", gl.Schedule)
	}
	if n := len(gs.Schedule); n != 13 || gs.Schedule[12].Status != domain.ScheduleScheduled || !gs.Schedule[12].Amount.Equal(decimal.RequireFromString("150.00")) ||
		!gs.Schedule[3].Amount.Equal(decimal.RequireFromString("150.00")) || !gs.Schedule[11].Amount.Equal(decimal.RequireFromString("150.00")) || gs.Schedule[2].Amount.String() != "100" {
		t.Fatalf("support schedule = %+v", gs.Schedule)
	}
	if n := outboxCount(t, pool, rev.ContractAllocated); n != 2 {
		t.Errorf("contract.allocated events = %d, want 2", n)
	}
	var recognizedEvents int
	_ = pool.QueryRow(context.Background(), `select count(*) from outbox where type = $1 and payload->'data'->>'amount' = '400.00'`, rev.ScheduleRecognized).Scan(&recognizedEvents)
	if recognizedEvents != 1 {
		t.Errorf("the licence catch-up was published %d times", recognizedEvents)
	}
	_ = sup

	recognizeThrough(t, s, ctx, le, 12)
	done, err := s.GetContract(ctx, c.ID)
	if err != nil || done.Status != domain.ContractComplete || !done.Recognized.Equal(decimal.RequireFromString("3000.00")) {
		t.Fatalf("after the term: %s recognized %s (%v)", done.Status, done.Recognized, err)
	}
	// March: the modification adds 1,000.00 in its own period; the year closes the deferred revenue at zero.
	march, err := s.RollForward(ctx, le, "USD", day(2027, time.March, 1), day(2027, time.March, 31))
	if err != nil || !march.Additions.Equal(decimal.RequireFromString("1000.00")) {
		t.Errorf("March roll-forward additions = %s (%v), want the modification's 1000.00", march.Additions, err)
	}
	year, err := s.RollForward(ctx, le, "USD", day(2027, time.January, 1), day(2027, time.December, 31))
	if err != nil || !year.Additions.Equal(decimal.RequireFromString("3000.00")) || !year.Closing.IsZero() {
		t.Errorf("year roll-forward = %+v (%v), want additions 3000.00 and closing 0", year, err)
	}
}

func TestModifyContract_ProspectiveLeavesRecognizedRevenueAndRefusesCreditedContracts(t *testing.T) {
	s, pool := testStore(t)
	ctx := tenantCtx(1, "")
	le := ids.New()
	c, err := s.CreateContract(ctx, supportAndLicence(t, le))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SatisfyMilestone(ctx, c.Obligations[0].ID, day(2027, time.January, 15), 0); err != nil {
		t.Fatal(err)
	}
	recognizeThrough(t, s, ctx, le, 3)
	got, mod, err := s.ModifyContract(ctx, c.ID, ModificationRequest{Type: domain.ModProspective, EffectiveDate: day(2027, time.March, 31),
		PriceChange: decimal.RequireFromString("-90.00")})
	if err != nil {
		t.Fatal(err)
	}
	// 2,000.00 - 90.00 = 1,910.00; licence 800.00 and support's 300.00 are recognized, so 810.00 spreads over 9 months: 90.00 each.
	sup := got.Obligations[1]
	if !mod.CatchUp.IsZero() || !sup.Allocated.Equal(decimal.RequireFromString("1110.00")) || len(sup.Schedule) != 12 || !sup.Schedule[3].Amount.Equal(decimal.RequireFromString("90.00")) {
		t.Fatalf("support %s lines %d %+v", sup.Allocated, len(sup.Schedule), sup.Schedule)
	}
	if sup.Schedule[0].Status != domain.ScheduleRecognized || !sup.Schedule[0].Amount.Equal(decimal.RequireFromString("100.00")) {
		t.Errorf("a recognized line changed: %+v", sup.Schedule[0])
	}
	// A contract that a credit note already moved is refused.
	if _, err := pool.Exec(context.Background(), `insert into credit_note_reversal (tenant_id, id, legal_entity_id, contract_id, credit_note_id, credit_note_number,
		line_no, original_line_no, product_id, currency, posting_date, deferred_amount, deferred_reduced, revenue_reversed, obligations, event_id)
		values ($1, $2, $3, $4, $5, 'CN-1', 1, 1, $6, 'USD', '2027-04-02', 10, 10, 0, '[]', 'e1')`,
		testkit.TenantID(1), ids.New(), le, c.ID, ids.New(), ids.New()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ModifyContract(ctx, c.ID, ModificationRequest{Type: domain.ModProspective, EffectiveDate: day(2027, time.April, 30),
		PriceChange: decimal.RequireFromString("10.00")}); problemCode(err) != CodeModificationAfterCredit {
		t.Fatalf("credited contract: %v", err)
	}
}
