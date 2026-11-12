package domain

import (
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func day(s string) *time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return &t
}

// ratable12 is a 12-month 2027 obligation of 1,200.00: 100.00 a month, the first `recognized` months recognized.
func ratable12(id string, recognized int) ModObligation {
	o := ModObligation{ID: id, Method: MethodRatable, SSP: dec("1200.00"), SSPSource: "EXPLICIT", Allocated: dec("1200.00")}
	for m := 1; m <= 12; m++ {
		end := time.Date(2027, time.Month(m)+1, 0, 0, 0, 0, 0, time.UTC)
		st := ScheduleScheduled
		if m <= recognized {
			st = ScheduleRecognized
		}
		o.Lines = append(o.Lines, ModLine{ID: fmt.Sprintf("%s-%02d", id, m), Date: &end, Amount: dec("100.00"), Status: st})
	}
	return o
}

func amounts(c ObligationChange) map[string]string {
	out := map[string]string{}
	for _, l := range c.Lines {
		out[l.ID] = l.Amount.StringFixed(2)
	}
	return out
}

// Worked example 1 (ASC 606-10-25-13(b)): a 12-month subscription of 1,200.00, three months recognized, the price rises by 120.00 on
// 2027-03-31 and the remaining service is not distinct from what was delivered. New allocation 1,320.00; progress 3/12, so revenue to
// date should be 330.00 against 300.00 recognized: a catch-up of 30.00, and the nine months left carry 990.00 (110.00 each).
func TestModify_CatchUpOnAPriceIncrease(t *testing.T) {
	res, err := Modify(ModificationInput{Type: ModCatchUp, EffectiveDate: *day("2027-03-31"), Currency: "USD", TransactionPrice: dec("1200.00"),
		PriceChange: dec("120.00"), Obligations: []ModObligation{ratable12("a", 3)}})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Obligations[0]
	if !res.TransactionPriceAfter.Equal(dec("1320.00")) || !c.AllocatedAfter.Equal(dec("1320.00")) || !c.Progress.Equal(dec("0.25")) {
		t.Fatalf("result = %+v", res)
	}
	if !c.CatchUp.Equal(dec("30.00")) || !res.CatchUp.Equal(dec("30.00")) {
		t.Fatalf("catch-up = %s", c.CatchUp)
	}
	if c.NewLine == nil || c.NewLine.Recognized || !c.NewLine.Amount.Equal(dec("30.00")) || !c.NewLine.Date.Equal(*day("2027-03-31")) {
		t.Fatalf("new line = %+v", c.NewLine)
	}
	got := amounts(c)
	if len(got) != 9 || got["a-04"] != "110.00" || got["a-12"] != "110.00" {
		t.Fatalf("future lines = %v", got)
	}
	// 300.00 recognized + 30.00 catch-up + 9 x 110.00 = 1,320.00 to the cent.
	total := dec("300.00").Add(c.NewLine.Amount)
	for _, l := range c.Lines {
		total = total.Add(l.Amount)
	}
	if !total.Equal(dec("1320.00")) {
		t.Errorf("schedule adds up to %s", total)
	}
}

// Worked example 2 (ASC 606-10-25-13(a)): the same price rise, but the remaining months are distinct, so the 1,020.00 still to be
// recognized (1,320.00 less the 300.00 already recognized) is spread over the nine months left: 113.33 eight times and 113.36 in the
// last (the residual); no catch-up and the recognized 300.00 stays as it is.
func TestModify_ProspectiveSpreadsTheRemainingConsiderationOverTheMonthsLeft(t *testing.T) {
	res, err := Modify(ModificationInput{Type: ModProspective, EffectiveDate: *day("2027-03-31"), Currency: "USD", TransactionPrice: dec("1200.00"),
		PriceChange: dec("120.00"), Obligations: []ModObligation{ratable12("a", 3)}})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Obligations[0]
	if !c.CatchUp.IsZero() || c.NewLine != nil || !c.AllocatedAfter.Equal(dec("1320.00")) {
		t.Fatalf("change = %+v", c)
	}
	got := amounts(c)
	if len(got) != 9 || got["a-04"] != "113.33" || got["a-11"] != "113.33" || got["a-12"] != "113.36" {
		t.Fatalf("lines = %v", got)
	}
	total := dec("300.00")
	for _, l := range c.Lines {
		total = total.Add(l.Amount)
	}
	if !total.Equal(dec("1320.00")) {
		t.Errorf("schedule adds up to %s", total)
	}
}

// Worked example 3: a bundle of a perpetual licence (milestone, SSP 800.00, satisfied) and a 12-month support plan (SSP 1,200.00, three
// months recognized). The contract price rises by 1,000.00 to 3,000.00. The pool 3,000.00 over SSPs 800 and 1,200 gives 1,200.00 and
// 1,800.00. The licence is fully transferred: a catch-up of 400.00 is recognized at once. The support is 25% transferred: 450.00 to
// date against 300.00 recognized, a catch-up of 150.00, and 1,350.00 over the nine months left (150.00 each).
func TestModify_CatchUpAcrossABundleCatchesUpTheSatisfiedMilestone(t *testing.T) {
	licence := ModObligation{ID: "lic", Method: MethodMilestone, SSP: dec("800.00"), SSPSource: "EXPLICIT", Allocated: dec("800.00"),
		Lines: []ModLine{{ID: "lic-1", Date: day("2027-01-15"), Amount: dec("800.00"), Status: ScheduleRecognized}}}
	res, err := Modify(ModificationInput{Type: ModCatchUp, EffectiveDate: *day("2027-03-31"), Currency: "USD", TransactionPrice: dec("2000.00"),
		PriceChange: dec("1000.00"), Obligations: []ModObligation{licence, ratable12("sup", 3)}})
	if err != nil {
		t.Fatal(err)
	}
	lic, sup := res.Obligations[0], res.Obligations[1]
	if !lic.AllocatedAfter.Equal(dec("1200.00")) || !sup.AllocatedAfter.Equal(dec("1800.00")) {
		t.Fatalf("allocation = %s / %s", lic.AllocatedAfter, sup.AllocatedAfter)
	}
	if !lic.CatchUp.Equal(dec("400.00")) || lic.NewLine == nil || !lic.NewLine.Recognized || len(lic.Lines) != 0 {
		t.Fatalf("licence = %+v", lic)
	}
	if !sup.CatchUp.Equal(dec("150.00")) || !res.CatchUp.Equal(dec("550.00")) {
		t.Fatalf("support catch-up = %s total %s", sup.CatchUp, res.CatchUp)
	}
	if got := amounts(sup); len(got) != 9 || got["sup-04"] != "150.00" || got["sup-12"] != "150.00" {
		t.Fatalf("support lines = %v", got)
	}
}

// Worked example 4 (ASC 606-10-25-13(b), IFRS 15.21(b)): a 12-month subscription of 1,200.00 with six months recognized (600.00)
// is cut by 700.00 to 500.00 on 2027-06-30, and the remaining months are not distinct. Revenue to date at the new price is 500.00 x
// 6/12 = 250.00 against 600.00 recognized: a negative cumulative catch-up of 350.00 comes back out of revenue at the date, and the six
// months left carry the other 250.00 (41.67 five times, 41.65 last). 600.00 - 350.00 + 250.00 = 500.00.
func TestModify_CatchUpBelowRecognizedReversesRevenue(t *testing.T) {
	res, err := Modify(ModificationInput{Type: ModCatchUp, EffectiveDate: *day("2027-06-30"), Currency: "USD", TransactionPrice: dec("1200.00"),
		PriceChange: dec("-700.00"), Obligations: []ModObligation{ratable12("a", 6)}})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Obligations[0]
	if !c.Reversal.Equal(dec("350.00")) || !res.Reversal.Equal(dec("350.00")) || !c.CatchUp.Equal(dec("-350.00")) || c.NewLine != nil {
		t.Fatalf("reversal %s catch-up %s new line %+v", c.Reversal, c.CatchUp, c.NewLine)
	}
	got := amounts(c)
	if len(got) != 6 || got["a-07"] != "41.67" || got["a-12"] != "41.65" {
		t.Fatalf("future lines = %v", got)
	}
	if !c.AllocatedAfter.Equal(dec("500.00")) {
		t.Fatalf("allocated after = %s", c.AllocatedAfter)
	}
}

// Worked example 5 (IFRS 15.88, ASC 606-10-32-43): the same cut accounted for prospectively. The remaining consideration would be
// 500.00 - 600.00 = -100.00: the 100.00 is a price concession on the six months already transferred and is taken out of revenue at the
// date; the six months left are delivered for nothing. 600.00 - 100.00 = 500.00.
func TestModify_ProspectiveBelowRecognizedIsAConcessionOnTransferredPerformance(t *testing.T) {
	res, err := Modify(ModificationInput{Type: ModProspective, EffectiveDate: *day("2027-06-30"), Currency: "USD", TransactionPrice: dec("1200.00"),
		PriceChange: dec("-700.00"), Obligations: []ModObligation{ratable12("a", 6)}})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Obligations[0]
	if !c.Reversal.Equal(dec("100.00")) || !c.AllocatedAfter.Equal(dec("500.00")) {
		t.Fatalf("reversal %s allocated %s", c.Reversal, c.AllocatedAfter)
	}
	for id, amt := range amounts(c) {
		if amt != "0.00" {
			t.Errorf("line %s = %s, want 0.00", id, amt)
		}
	}
}

// Worked example 6 (ASC 606-10-25-13(a), partial termination): two 12-month services of 1,200.00 each, three months recognized on both.
// Service b is removed from April and the price falls by 900.00 to 1,500.00. The 900.00 still to recognize (1,500.00 - 600.00) goes to a
// alone (b has nothing left promised): 100.00 a month as before; b keeps the 300.00 it recognized and its nine lines are cancelled.
func TestModify_ProspectiveRemovesAnObligation(t *testing.T) {
	res, err := Modify(ModificationInput{Type: ModProspective, EffectiveDate: *day("2027-03-31"), Currency: "USD", TransactionPrice: dec("2400.00"),
		PriceChange: dec("-900.00"), Scope: map[string]decimal.Decimal{"b": decimal.Zero}, Obligations: []ModObligation{ratable12("a", 3), ratable12("b", 3)}})
	if err != nil {
		t.Fatal(err)
	}
	a, b := res.Obligations[0], res.Obligations[1]
	if !a.AllocatedAfter.Equal(dec("1200.00")) || amounts(a)["a-04"] != "100.00" {
		t.Fatalf("a = %+v", a)
	}
	if !b.Removed || !b.AllocatedAfter.Equal(dec("300.00")) || !b.Reversal.IsZero() || !b.SSPAfter.Equal(dec("300.00")) {
		t.Fatalf("b = %+v", b)
	}
	for id, amt := range amounts(b) {
		if amt != "0.00" {
			t.Errorf("removed line %s = %s", id, amt)
		}
	}
}

// Worked example 7 (reduction, catch-up): the remaining nine months of a 1,200.00 service are halved in scope and the price falls by
// 450.00 to 750.00. The SSP scales to what stays promised (1,200.00 x 750/1,200 = 750.00) and progress becomes 300/750 = 0.4: revenue to
// date 300.00 equals what is recognized, so there is no catch-up, and the nine months left carry 50.00 each.
func TestModify_CatchUpReducesTheRemainingScope(t *testing.T) {
	res, err := Modify(ModificationInput{Type: ModCatchUp, EffectiveDate: *day("2027-03-31"), Currency: "USD", TransactionPrice: dec("1200.00"),
		PriceChange: dec("-450.00"), Scope: map[string]decimal.Decimal{"a": dec("0.5")}, Obligations: []ModObligation{ratable12("a", 3)}})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Obligations[0]
	if !c.Progress.Equal(dec("0.4")) || !c.CatchUp.IsZero() || !c.Reversal.IsZero() || !c.SSPAfter.Equal(dec("750.00")) {
		t.Fatalf("change = %+v", c)
	}
	if got := amounts(c); got["a-04"] != "50.00" || got["a-12"] != "50.00" {
		t.Fatalf("lines = %v", got)
	}
}

// Worked example 8 (removal, catch-up across a bundle): a satisfied licence (SSP 800.00) and a support plan (1,200.00, three months
// recognized). Support is removed from April and the price falls by 1,200.00 to 800.00. Support's SSP scales to the 300.00 delivered;
// 800.00 over 800 : 300 gives 581.82 and 218.18. The licence recognized 800.00, so 218.18 comes back out; support recognized 300.00
// against 218.18, so 81.82 comes back out and its nine lines are cancelled. Revenue ends at 800.00.
func TestModify_CatchUpRemovalReallocatesAcrossTheBundle(t *testing.T) {
	licence := ModObligation{ID: "lic", Method: MethodMilestone, SSP: dec("800.00"), SSPSource: "EXPLICIT", Allocated: dec("800.00"),
		Lines: []ModLine{{ID: "lic-1", Date: day("2027-01-15"), Amount: dec("800.00"), Status: ScheduleRecognized}}}
	res, err := Modify(ModificationInput{Type: ModCatchUp, EffectiveDate: *day("2027-03-31"), Currency: "USD", TransactionPrice: dec("2000.00"),
		PriceChange: dec("-1200.00"), Scope: map[string]decimal.Decimal{"sup": decimal.Zero}, Obligations: []ModObligation{licence, ratable12("sup", 3)}})
	if err != nil {
		t.Fatal(err)
	}
	lic, sup := res.Obligations[0], res.Obligations[1]
	if !lic.AllocatedAfter.Equal(dec("581.82")) || !lic.Reversal.Equal(dec("218.18")) {
		t.Fatalf("licence = %+v", lic)
	}
	if !sup.AllocatedAfter.Equal(dec("218.18")) || !sup.Reversal.Equal(dec("81.82")) || !sup.Removed {
		t.Fatalf("support = %+v", sup)
	}
	if !res.Reversal.Equal(dec("300.00")) {
		t.Fatalf("total reversal = %s", res.Reversal)
	}
}

// What revrec cannot take back: a price below what the point-in-time goods were billed at is a credit note (REV-0402), and a scope
// outside 0..1 is refused.
func TestModify_ACutBelowPointInTimeRevenueIsRefused(t *testing.T) {
	goods := ModObligation{ID: "g", Method: MethodPointInTime, SSP: dec("500.00"), SSPSource: "EXPLICIT", Allocated: dec("500.00"),
		Lines: []ModLine{{ID: "g-1", Date: day("2027-01-05"), Amount: dec("500.00"), Status: ScheduleRecognized}}}
	for _, typ := range []string{ModCatchUp, ModProspective} {
		_, err := Modify(ModificationInput{Type: typ, EffectiveDate: *day("2027-06-30"), Currency: "USD", TransactionPrice: dec("1700.00"),
			PriceChange: dec("-1300.00"), Obligations: []ModObligation{goods, ratable12("a", 6)}})
		if e, ok := err.(*Error); !ok || e.Code != CodeModBelowRecognized {
			t.Fatalf("%s cut: %v", typ, err)
		}
	}
	if _, err := Modify(ModificationInput{Type: ModCatchUp, EffectiveDate: *day("2027-06-30"), Currency: "USD", TransactionPrice: dec("1200.00"),
		PriceChange: dec("-1"), Scope: map[string]decimal.Decimal{"a": dec("1.5")}, Obligations: []ModObligation{ratable12("a", 6)}}); err == nil {
		t.Fatal("scope 1.5 accepted")
	}
}

// A credited contract: 200.00 of recognized revenue was reversed by a credit note, so the obligation holds 400.00 net of six months. A
// catch-up to 1,000.00 at 6/12 gives 500.00 to date: a catch-up of 100.00 on the net figure.
func TestModify_CatchUpNetsCreditNoteReversals(t *testing.T) {
	o := ratable12("a", 6)
	o.Reversed, o.Allocated = dec("200.00"), dec("1000.00")
	res, err := Modify(ModificationInput{Type: ModCatchUp, EffectiveDate: *day("2027-06-30"), Currency: "USD", TransactionPrice: dec("1000.00"),
		PriceChange: dec("0"), NewSSP: map[string]decimal.Decimal{"a": dec("1000.00")}, Obligations: []ModObligation{o}})
	if err != nil {
		t.Fatal(err)
	}
	if c := res.Obligations[0]; !c.CatchUp.Equal(dec("100.00")) || !c.Reversal.IsZero() {
		t.Fatalf("change = %+v", c)
	}
}

// A price cut that stays above what is recognized works in both ways: prospective lowers the months left; catch-up here has less to
// recognize at the date than was recognized only if the cut is deep, so a small one is a negative catch-up and is refused, while a
// price rise on a fully recognized obligation is caught up in one line.
func TestModify_ProspectiveCutAndFullyRecognizedCatchUp(t *testing.T) {
	res, err := Modify(ModificationInput{Type: ModProspective, EffectiveDate: *day("2027-06-30"), Currency: "USD", TransactionPrice: dec("1200.00"),
		PriceChange: dec("-300.00"), Obligations: []ModObligation{ratable12("a", 6)}})
	if err != nil {
		t.Fatal(err)
	}
	if got := amounts(res.Obligations[0]); len(got) != 6 || got["a-07"] != "50.00" || got["a-12"] != "50.00" {
		t.Errorf("prospective cut lines = %v", got)
	}
	done, err := Modify(ModificationInput{Type: ModCatchUp, EffectiveDate: *day("2027-12-31"), Currency: "USD", TransactionPrice: dec("1200.00"),
		PriceChange: dec("60.00"), Obligations: []ModObligation{ratable12("a", 12)}})
	if err != nil {
		t.Fatal(err)
	}
	if c := done.Obligations[0]; !c.CatchUp.Equal(dec("60.00")) || c.NewLine == nil || len(c.Lines) != 0 {
		t.Errorf("fully recognized catch-up = %+v", c)
	}
	if _, err := Modify(ModificationInput{Type: ModProspective, EffectiveDate: *day("2027-12-31"), Currency: "USD", TransactionPrice: dec("1200.00"),
		PriceChange: dec("60.00"), Obligations: []ModObligation{ratable12("a", 12)}}); err == nil {
		t.Error("a prospective change with nothing left was accepted")
	}
}

// Obligations booked by the invoice (point in time) are left alone and their price is taken out of the pool.
func TestModify_PointInTimeObligationsAreKept(t *testing.T) {
	goods := ModObligation{ID: "g", Method: MethodPointInTime, SSP: dec("500.00"), SSPSource: "EXPLICIT", Allocated: dec("500.00"),
		Lines: []ModLine{{ID: "g-1", Date: day("2027-01-05"), Amount: dec("500.00"), Status: ScheduleRecognized}}}
	res, err := Modify(ModificationInput{Type: ModCatchUp, EffectiveDate: *day("2027-03-31"), Currency: "USD", TransactionPrice: dec("1700.00"),
		PriceChange: dec("120.00"), Obligations: []ModObligation{goods, ratable12("a", 3)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Obligations) != 1 || res.Obligations[0].ID != "a" || !res.Obligations[0].AllocatedAfter.Equal(dec("1320.00")) {
		t.Fatalf("result = %+v", res)
	}
}

// An SSP revision applies at the modification date: the unsatisfied remainder is weighted by SSP and the share left.
func TestModify_ProspectiveSSPRevisionShiftsTheRemainingConsideration(t *testing.T) {
	a, b := ratable12("a", 0), ratable12("b", 0)
	res, err := Modify(ModificationInput{Type: ModProspective, EffectiveDate: *day("2027-01-01"), Currency: "USD", TransactionPrice: dec("2400.00"),
		PriceChange: dec("0"), NewSSP: map[string]decimal.Decimal{"b": dec("3600.00")}, Obligations: []ModObligation{a, b}})
	if err != nil {
		t.Fatal(err)
	}
	// 2,400.00 over SSPs 1,200 : 3,600 = 600.00 : 1,800.00.
	if !res.Obligations[0].AllocatedAfter.Equal(dec("600.00")) || !res.Obligations[1].AllocatedAfter.Equal(dec("1800.00")) || !res.Obligations[1].SSPAfter.Equal(dec("3600.00")) {
		t.Fatalf("allocation = %s / %s", res.Obligations[0].AllocatedAfter, res.Obligations[1].AllocatedAfter)
	}
	if got := amounts(res.Obligations[0]); got["a-01"] != "50.00" {
		t.Errorf("a lines = %v", got)
	}
}

// Earned lines not yet recognized: two months recognized and March (dated on the effective date) still SCHEDULED. Revenue to date is 330.00,
// 200.00 is recognized and March's old 100.00 will be recognized anyway, so the catch-up is 30.00, not 130.00; March's line carries the whole
// 130.00 and no new line is added.
func TestModify_CatchUpDoesNotCountEarnedLinesThatAreNotRecognizedYet(t *testing.T) {
	res, err := Modify(ModificationInput{Type: ModCatchUp, EffectiveDate: *day("2027-03-31"), Currency: "USD", TransactionPrice: dec("1200.00"),
		PriceChange: dec("120.00"), Obligations: []ModObligation{ratable12("a", 2)}})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Obligations[0]
	if !c.CatchUp.Equal(dec("30.00")) || !res.CatchUp.Equal(dec("30.00")) || c.NewLine != nil {
		t.Fatalf("catch-up %s total %s new line %+v", c.CatchUp, res.CatchUp, c.NewLine)
	}
	got := amounts(c)
	if got["a-03"] != "130.00" || got["a-04"] != "110.00" || len(got) != 10 {
		t.Fatalf("lines = %v", got)
	}
}

// credited12 is ratable12 with six months recognized and a credit note that took 300.00 of deferred revenue off the six months left
// (50.00 each, credited_amount 50.00 each).
func credited12(id string) ModObligation {
	o := ratable12(id, 6)
	for i := 6; i < 12; i++ {
		o.Lines[i].Amount, o.Lines[i].Credited = dec("50.00"), dec("50.00")
	}
	o.Allocated = dec("900.00")
	return o
}

// Review case: a 1,200.00 ratable obligation, 600.00 recognized over six months, a credit note took 300.00 off the six months left. A
// catch-up of +100.00 at the end of month 6 measures progress by time, 0.5 (not 600/900 from the credited amounts): revenue to date is
// 1,000.00 x 0.5 = 500.00 against 600.00 recognized, so 100.00 comes back out.
func TestModify_CatchUpOnACreditedObligationMeasuresProgressBeforeTheCredit(t *testing.T) {
	res, err := Modify(ModificationInput{Type: ModCatchUp, EffectiveDate: *day("2027-06-30"), Currency: "USD", TransactionPrice: dec("900.00"),
		PriceChange: dec("100.00"), Obligations: []ModObligation{credited12("a")}})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Obligations[0]
	if !c.Progress.Equal(dec("0.5")) || !c.AllocatedAfter.Equal(dec("1000.00")) || !c.Reversal.Equal(dec("100.00")) || !c.CatchUp.Equal(dec("-100.00")) {
		t.Fatalf("change = %+v", c)
	}
	// A line credit notes took to zero still counts in the measure.
	o := credited12("b")
	o.Lines[11].Amount, o.Lines[11].Credited, o.Lines[11].Status = dec("0"), dec("100.00"), ScheduleReversed
	if p := Progress(o, *day("2027-06-30")); !p.Equal(dec("0.5")) {
		t.Fatalf("progress with a credit-reversed line = %s", p)
	}
}

// The same credit skews prospective weights too: a credited and an uncredited obligation, both half delivered with SSP 1,200.00, share the
// remaining 1,200.00 equally (600.00 each), not 480.00 / 720.00 by their credited amounts.
func TestModify_ProspectiveWeightsACreditedObligationByItsMeasure(t *testing.T) {
	res, err := Modify(ModificationInput{Type: ModProspective, EffectiveDate: *day("2027-06-30"), Currency: "USD", TransactionPrice: dec("2100.00"),
		PriceChange: dec("300.00"), Obligations: []ModObligation{credited12("a"), ratable12("b", 6)}})
	if err != nil {
		t.Fatal(err)
	}
	a, b := res.Obligations[0], res.Obligations[1]
	if !a.AllocatedAfter.Equal(dec("1200.00")) || !b.AllocatedAfter.Equal(dec("1200.00")) {
		t.Fatalf("allocation %s / %s", a.AllocatedAfter, b.AllocatedAfter)
	}
}
