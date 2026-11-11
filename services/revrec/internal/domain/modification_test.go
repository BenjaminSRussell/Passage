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

// A change that would take revenue back out of what is already recognized is a credit note, not a modification.
func TestModify_ACutBelowRecognizedRevenueIsRefused(t *testing.T) {
	_, err := Modify(ModificationInput{Type: ModCatchUp, EffectiveDate: *day("2027-06-30"), Currency: "USD", TransactionPrice: dec("1200.00"),
		PriceChange: dec("-700.00"), Obligations: []ModObligation{ratable12("a", 6)}})
	if e, ok := err.(*Error); !ok || e.Code != CodeModBelowRecognized {
		t.Fatalf("catch-up cut: %v", err)
	}
	_, err = Modify(ModificationInput{Type: ModProspective, EffectiveDate: *day("2027-06-30"), Currency: "USD", TransactionPrice: dec("1200.00"),
		PriceChange: dec("-700.00"), Obligations: []ModObligation{ratable12("a", 6)}})
	if e, ok := err.(*Error); !ok || e.Code != CodeModBelowRecognized {
		t.Fatalf("prospective cut: %v", err)
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
