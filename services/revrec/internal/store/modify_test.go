package store

import (
	"context"
	"strings"
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
		PriceChange: decimal.RequireFromString("-100.00"), Scope: map[string]decimal.Decimal{ids.New(): decimal.Zero}}); problemCode(err) != "REV-0001" {
		t.Fatalf("a scope for an obligation of another contract: %v", err)
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
	// A contract that a credit note already moved is modified on its amounts net of the credit note: April's 90.00 was credited
	// (the line is REVERSED). Net price 1,820.00 + 80.00 = 1,900.00; the licence's 800.00 and support's 300.00 are recognized, so
	// 800.00 spreads over the eight months left (100.00 each). Stored amounts add the credit back: support 1,190.00, price 1,990.00.
	if _, err := pool.Exec(context.Background(), `update revenue_schedule set amount = 0, credited_amount = 90, status = 'REVERSED' where id = $1`,
		sup.Schedule[3].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `insert into credit_note_reversal (tenant_id, id, legal_entity_id, contract_id, credit_note_id, credit_note_number,
		line_no, original_line_no, product_id, currency, posting_date, deferred_amount, deferred_reduced, revenue_reversed, obligations, event_id)
		values ($1, $2, $3, $4, $5, 'CN-1', 1, 1, $6, 'USD', '2027-04-02', 90, 90, 0, $7::jsonb, 'e1')`,
		testkit.TenantID(1), ids.New(), le, c.ID, ids.New(), ids.New(),
		`[{"obligationId":"`+sup.ID+`","share":"90","reduced":"90","reversed":"0"}]`); err != nil {
		t.Fatal(err)
	}
	after, mod2, err := s.ModifyContract(ctx, c.ID, ModificationRequest{Type: domain.ModProspective, EffectiveDate: day(2027, time.April, 30),
		PriceChange: decimal.RequireFromString("80.00")})
	if err != nil {
		t.Fatalf("credited contract: %v", err)
	}
	sup2 := after.Obligations[1]
	if !after.TransactionPrice.Equal(decimal.RequireFromString("1990.00")) || !sup2.Allocated.Equal(decimal.RequireFromString("1190.00")) ||
		!mod2.PriceBefore.Equal(decimal.RequireFromString("1820.00")) {
		t.Fatalf("credited contract price %s support %s before %s", after.TransactionPrice, sup2.Allocated, mod2.PriceBefore)
	}
	if !sup2.Schedule[4].Amount.Equal(decimal.RequireFromString("100.00")) || sup2.Schedule[3].Status != domain.ScheduleReversed || !sup2.Schedule[3].Amount.IsZero() {
		t.Fatalf("credited support schedule %+v", sup2.Schedule)
	}
}

// Worked example 8 through Postgres: support is removed from April (scope 0) with the price cut by 1,200.00 to 800.00, a catch-up. The
// licence keeps 581.82 and 218.18 of its revenue comes back out; support keeps 218.18 of the 300.00 it recognized (81.82 back out) and
// its nine lines are cancelled, so the contract completes. The reversals are posted Dr 4000 / Cr 2400 with the credit-note ones, the
// year's roll-forward closes at zero, and the price decrease is queued for billing.
func TestModifyContract_RemovalBelowRecognizedReversesRevenueAndQueuesBilling(t *testing.T) {
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
	got, mod, err := s.ModifyContract(ctx, c.ID, ModificationRequest{Type: domain.ModCatchUp, EffectiveDate: day(2027, time.March, 31),
		PriceChange: decimal.RequireFromString("-1200.00"), Scope: map[string]decimal.Decimal{sup.ID: decimal.Zero}, Reason: "support cancelled"})
	if err != nil {
		t.Fatal(err)
	}
	if !mod.Reversal.Equal(decimal.RequireFromString("300.00")) || mod.BillingID == "" || got.Status != domain.ContractComplete {
		t.Fatalf("modification %+v status %s", mod, got.Status)
	}
	if !got.Obligations[0].Allocated.Equal(decimal.RequireFromString("581.82")) || !got.Obligations[1].Allocated.Equal(decimal.RequireFromString("218.18")) {
		t.Fatalf("allocation %s / %s", got.Obligations[0].Allocated, got.Obligations[1].Allocated)
	}
	for _, l := range got.Obligations[1].Schedule[3:] {
		if l.Status != domain.ScheduleReversed || !l.Amount.IsZero() {
			t.Fatalf("removed support line %+v", l)
		}
	}
	var body string
	if err := pool.QueryRow(context.Background(), `select body::text from modification_billing where modification_id = $1`, mod.ID).Scan(&body); err != nil ||
		!strings.Contains(body, `"priceChange": "-1200.00"`) || !strings.Contains(body, got.OrderID) || !strings.Contains(body, `"amount": "-981.82"`) {
		t.Fatalf("billing request %s (%v)", body, err)
	}
	ledger := ids.New()
	due, other, err := s.DueReversals(ctx, le, ledger, time.Now(), false)
	if err != nil || len(due) != 2 || len(other) != 0 {
		t.Fatalf("due reversals %+v (%v)", due, err)
	}
	total := decimal.Zero
	for _, rp := range due {
		if rp.ModificationID != mod.ID || rp.CreditNoteID != "" || rp.PostingDate.Format(dateLayout) != "2027-03-31" {
			t.Fatalf("reversal %+v", rp)
		}
		total = total.Add(rp.Amount)
		if err := s.CompleteReversal(ctx, rp.ID, "entry-"+rp.ID); err != nil {
			t.Fatal(err)
		}
	}
	if !total.Equal(decimal.RequireFromString("300.00")) {
		t.Fatalf("reversed %s", total)
	}
	if again, _, _ := s.DueReversals(ctx, le, ledger, time.Now(), false); len(again) != 0 {
		t.Fatalf("posted reversals offered again: %+v", again)
	}
	year, err := s.RollForward(ctx, le, "USD", day(2027, time.January, 1), day(2027, time.December, 31))
	if err != nil || !year.Additions.Equal(decimal.RequireFromString("800.00")) || !year.Reversed.Equal(decimal.RequireFromString("300.00")) ||
		!year.Closing.IsZero() || !year.ModificationCredits.Equal(decimal.RequireFromString("1200.00")) {
		t.Fatalf("year roll-forward %+v (%v)", year, err)
	}
	// A credit note on the reduced support can no longer reverse the revenue the modification took back.
	var left string
	if err := pool.QueryRow(context.Background(), `select 300 - coalesce(sum(revenue_reversed), 0) from modification_reversal where obligation_id = $1`, sup.ID).Scan(&left); err != nil || left != "218.180000" {
		t.Fatalf("support revenue left %s (%v)", left, err)
	}
}
