package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func TestToleranceRuleValidate(t *testing.T) {
	cases := []struct {
		name string
		rule ToleranceRule
		ok   bool
	}{
		{"named total", ToleranceRule{Pack: PackOpenItems, Total: "open_items.amount", Amount: d("0.05"), Reason: "rounding of legacy conversion"}, true},
		{"pack default", ToleranceRule{Pack: PackOnhand, Total: AnyTotal, Amount: d("1"), Reason: "costing rounding"}, true},
		{"zero needs no reason", ToleranceRule{Pack: PackOnhand, Total: AnyTotal, Amount: d("0")}, true},
		{"positive without reason", ToleranceRule{Pack: PackOpenItems, Total: "open_items.amount", Amount: d("0.05")}, false},
		{"negative", ToleranceRule{Pack: PackOpenItems, Total: "open_items.amount", Amount: d("-1"), Reason: "x"}, false},
		{"total of another pack", ToleranceRule{Pack: PackOpenItems, Total: "onhand.value", Amount: d("1"), Reason: "x"}, false},
		{"seven decimals", ToleranceRule{Pack: PackOpenItems, Total: AnyTotal, Amount: d("0.0000001"), Reason: "x"}, false},
		{"unknown pack", ToleranceRule{Pack: "nope", Total: AnyTotal, Amount: d("1"), Reason: "x"}, false},
		{"a count is exact", ToleranceRule{Pack: PackOpenItems, Total: "open_items.count", Amount: d("1"), Reason: "x"}, false},
		{"a quantity is exact", ToleranceRule{Pack: PackOnhand, Total: OnhandQuantityName, Amount: d("1"), Reason: "x"}, false},
		{"a name the pack does not compute", ToleranceRule{Pack: PackOpenItems, Total: "open_items.amout", Amount: d("1"), Reason: "x"}, false},
		{"project_wip has no money total per currency", ToleranceRule{Pack: PackProjectWIP, Total: AnyTotal, Amount: d("1"), Reason: "x"}, false},
		{"a pack without totals yet", ToleranceRule{Pack: PackPayrollYTD, Total: AnyTotal, Amount: d("1"), Reason: "x"}, false},
		{"every listed total is accepted", ToleranceRule{Pack: PackGLBalances, Total: ControlAccountName, Amount: d("0.01"), Reason: "x"}, true},
	}
	for _, c := range cases {
		err := c.rule.Validate()
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v", c.name, err)
		}
		if err != nil && !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrUnknownPack) {
			t.Errorf("%s: err %v is not a domain error", c.name, err)
		}
	}
}

func TestToleranceSetFor(t *testing.T) {
	usd := map[string]string{"company": "USMF", "currency": "USD"}
	s := NewToleranceSet([]ToleranceRule{
		{Pack: PackOpenItems, Total: AnyTotal, Amount: d("0.10"), Reason: "pack"},
		{Pack: PackOpenItems, Total: "open_items.amount_by_account", Amount: d("0"), Reason: ""},
		{Pack: PackOnhand, Total: AnyTotal, Amount: d("5"), Reason: "costing"},
	})
	if got := s.For("open_items.amount", usd); !got.Amount.Equal(d("0.10")) || got.Reason != "pack" {
		t.Errorf("pack default: %+v", got)
	}
	if got := s.For("open_items.amount_by_account", usd); !got.Amount.IsZero() {
		t.Errorf("a named zero rule beats the pack default: %+v", got)
	}
	if got := s.For("open_items.count", map[string]string{"company": "USMF"}); !got.Amount.IsZero() {
		t.Errorf("the pack default must not reach a count: %+v", got)
	}
	if got := s.For(OnhandQuantityName, map[string]string{"company": "USMF", "item": "BOLT"}); !got.Amount.IsZero() {
		t.Errorf("the pack default must not reach a quantity: %+v", got)
	}
	if got := s.For("open_items.amount", map[string]string{"company": "USMF"}); !got.Amount.IsZero() {
		t.Errorf("a total without a currency compares exactly: %+v", got)
	}
	if got := s.For("gl_balances.balance", usd); !got.Amount.IsZero() {
		t.Errorf("another pack: %+v", got)
	}
	if got := ToleranceSet(nil).For("open_items.amount", usd); !got.Amount.IsZero() {
		t.Errorf("empty set: %+v", got)
	}
}

func TestReconcileOpenItems_ConfiguredTolerance(t *testing.T) {
	records := []Record{{LegacyID: "1", Party: "V1", Account: "2100", Currency: "USD", Amount: d("10.05")}}
	present := map[string]decimal.Decimal{"AR|1": d("10.00")}
	t.Run("without a tolerance the delta is a mismatch", func(t *testing.T) {
		r := validatedRun(t)
		if err := ReconcileOpenItems(r, records, records, 0, present); err != nil {
			t.Fatal(err)
		}
		if r.Status != StatusLoaded || !r.HasMismatch() {
			t.Fatalf("status %s totals %+v", r.Status, r.Totals)
		}
	})
	t.Run("a delta inside the tolerance reconciles and keeps the allowance on the total", func(t *testing.T) {
		r := validatedRun(t)
		r.Tolerances = NewToleranceSet([]ToleranceRule{{Pack: PackOpenItems, Total: "open_items.amount", Amount: d("0.05"), Reason: "conversion rounding"}})
		if err := ReconcileOpenItems(r, records, records, 0, present); err != nil {
			t.Fatal(err)
		}
		var within int
		for _, tot := range r.Totals {
			if tot.Status == StatusWithinTolerance {
				within++
				if tot.Name != "open_items.amount" || !tot.Tolerance.Amount.Equal(d("0.05")) || tot.Tolerance.Reason != "conversion rounding" {
					t.Errorf("total %+v", tot)
				}
			}
		}
		// open_items.amount is within; amount_by_account has no rule and still differs.
		if within != 1 || r.Status != StatusLoaded {
			t.Fatalf("within %d status %s totals %+v", within, r.Status, r.Totals)
		}
	})
	t.Run("a pack default covers every total", func(t *testing.T) {
		r := validatedRun(t)
		r.Tolerances = NewToleranceSet([]ToleranceRule{{Pack: PackOpenItems, Total: AnyTotal, Amount: d("0.05"), Reason: "rounding"}})
		if err := ReconcileOpenItems(r, records, records, 0, present); err != nil {
			t.Fatal(err)
		}
		if r.Status != StatusReconciled {
			t.Fatalf("status %s totals %+v", r.Status, r.Totals)
		}
	})
	t.Run("a delta beyond the tolerance is still a mismatch", func(t *testing.T) {
		r := validatedRun(t)
		r.Tolerances = NewToleranceSet([]ToleranceRule{{Pack: PackOpenItems, Total: AnyTotal, Amount: d("0.04"), Reason: "rounding"}})
		if err := ReconcileOpenItems(r, records, records, 0, present); err != nil {
			t.Fatal(err)
		}
		if r.Status != StatusLoaded {
			t.Fatalf("status %s", r.Status)
		}
	})
}

// The tolerance is an absolute delta per currency: two currencies each inside it reconcile even
// though their deltas add up to more, and one currency outside it holds the run back however
// small the other deltas are.
func TestReconcileOpenItems_ToleranceIsPerCurrency(t *testing.T) {
	records := []Record{
		{LegacyID: "1", Party: "V1", Account: "2100", Currency: "USD", Amount: d("10.04")},
		{LegacyID: "2", Party: "V1", Account: "2100", Currency: "EUR", Amount: d("20.04")},
	}
	present := map[string]decimal.Decimal{"AR|1": d("10.00"), "AR|2": d("20.00")}
	rules := NewToleranceSet([]ToleranceRule{{Pack: PackOpenItems, Total: AnyTotal, Amount: d("0.05"), Reason: "rounding"}})
	r := validatedRun(t)
	r.Tolerances = rules
	if err := ReconcileOpenItems(r, records, records, 0, present); err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusReconciled {
		t.Fatalf("0.04 in each currency is inside 0.05: %s %+v", r.Status, r.Totals)
	}
	records[1].Amount = d("20.06")
	r = validatedRun(t)
	r.Tolerances = rules
	if err := ReconcileOpenItems(r, records, records, 0, present); err != nil {
		t.Fatal(err)
	}
	for _, tot := range r.Totals {
		if tot.Key["currency"] == "EUR" && tot.Status != StatusMismatch {
			t.Errorf("EUR 0.06 is outside 0.05: %+v", tot)
		}
		if tot.Key["currency"] == "USD" && tot.Status != StatusWithinTolerance {
			t.Errorf("USD 0.04 is inside 0.05: %+v", tot)
		}
	}
	if r.Status != StatusLoaded {
		t.Fatalf("status %s", r.Status)
	}
}

// A release never reads the configured tolerances: the run's totals keep the tolerance they were
// reconciled with, and a total the release creates takes the same one, so widening a tolerance
// after a run started cannot reconcile that run.
func TestRelease_KeepsTheRunsTolerance(t *testing.T) {
	parties := map[string]struct{}{"V1": {}, "V2": {}}
	records := []Record{
		{LegacyID: "1", Party: "V1", Account: "2100", Currency: "USD", Amount: d("10.05")},
		{LegacyID: "2", Party: "BAD", Account: "2100", Currency: "USD", Amount: d("5")},
	}
	accepted, quarantined := Validate(records, parties)
	r := validatedRun(t)
	r.Pack = PackOpenItems
	r.Tolerances = NewToleranceSet([]ToleranceRule{{Pack: PackOpenItems, Total: AnyTotal, Amount: d("0.05"), Reason: "rounding"}})
	if err := ReconcileOpenItems(r, records, accepted, 0, map[string]decimal.Decimal{"AR|1": d("10.00")}); err != nil {
		t.Fatal(err)
	}
	r.RecordsQuarantined = len(quarantined)
	if r.Status != StatusLoaded {
		t.Fatalf("the quarantined line holds the run: %s", r.Status)
	}
	// The configuration is widened after the run; a release must not pick it up.
	r.Tolerances = NewToleranceSet([]ToleranceRule{{Pack: PackOpenItems, Total: AnyTotal, Amount: d("100"), Reason: "wide"}})
	if _, err := ReleaseOpenItem(r, records[1], Correction{Party: "V2", Account: "2200"}, parties); err != nil {
		t.Fatal(err)
	}
	created := false
	for _, tot := range r.Totals {
		if !tot.Tolerance.Amount.Equal(d("0.05")) && tolerable(tot.Name) {
			t.Errorf("total %+v does not keep the run's tolerance", tot)
		}
		if tot.Name == "open_items.amount_by_account" && tot.Key["account"] == "2200" {
			created = true
		}
	}
	if !created || r.Status != StatusReconciled {
		t.Fatalf("created %v status %s totals %+v", created, r.Status, r.Totals)
	}

	// A run reconciled without a tolerance stays exact through a release.
	r = validatedRun(t)
	r.Pack = PackOpenItems
	if err := ReconcileOpenItems(r, records, accepted, 0, map[string]decimal.Decimal{"AR|1": d("10.00")}); err != nil {
		t.Fatal(err)
	}
	r.RecordsQuarantined = len(quarantined)
	r.Tolerances = NewToleranceSet([]ToleranceRule{{Pack: PackOpenItems, Total: AnyTotal, Amount: d("100"), Reason: "wide"}})
	if _, err := ReleaseOpenItem(r, records[1], Correction{Party: "V2", Account: "2200"}, parties); err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusLoaded || !r.HasMismatch() {
		t.Fatalf("the 0.05 delta must still hold the run: %s %+v", r.Status, r.Totals)
	}
}
