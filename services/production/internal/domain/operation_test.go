package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestCheckOrderQuantity_Table(t *testing.T) {
	q := decimal.RequireFromString
	cases := []struct {
		name  string
		qty   string
		rules OrderQuantityRules
		code  string
	}{
		{"no rules", "7.5", OrderQuantityRules{}, ""},
		{"at the minimum", "10", OrderQuantityRules{Minimum: q("10")}, ""},
		{"below the minimum", "9.999999", OrderQuantityRules{Minimum: q("10")}, CodeOrderQuantity},
		{"at the maximum", "100", OrderQuantityRules{Maximum: q("100")}, ""},
		{"above the maximum", "100.000001", OrderQuantityRules{Maximum: q("100")}, CodeOrderQuantity},
		{"a multiple", "120", OrderQuantityRules{Multiple: q("12")}, ""},
		{"not a multiple", "121", OrderQuantityRules{Multiple: q("12")}, CodeOrderQuantity},
		{"a fractional multiple", "2.5", OrderQuantityRules{Multiple: q("0.5")}, ""},
		{"not a fractional multiple", "2.6", OrderQuantityRules{Multiple: q("0.5")}, CodeOrderQuantity},
		{"all three hold", "48", OrderQuantityRules{Minimum: q("24"), Maximum: q("96"), Multiple: q("12")}, ""},
		{"maximum below minimum", "50", OrderQuantityRules{Minimum: q("60"), Maximum: q("40")}, CodeInvalidInput},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckOrderQuantity(q(c.qty), c.rules)
			if c.code == "" {
				if err != nil {
					t.Fatalf("unexpected %v", err)
				}
				return
			}
			var de *Error
			if !errors.As(err, &de) || de.Code != c.code {
				t.Fatalf("err = %v, want %s", err, c.code)
			}
		})
	}
}

// A quantity passes exactly when it is within the bounds and a whole number of multiples.
func TestCheckOrderQuantity_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		step := rapid.Int64Range(1, 500).Draw(t, "step") // in thousandths
		k := rapid.Int64Range(1, 1000).Draw(t, "k")
		off := rapid.Int64Range(0, step-1).Draw(t, "off")
		multiple := decimal.New(step, -3)
		qty := decimal.New(step*k+off, -3)
		min := decimal.New(rapid.Int64Range(0, 600_000).Draw(t, "min"), -3)
		err := CheckOrderQuantity(qty, OrderQuantityRules{Minimum: min, Multiple: multiple})
		want := off == 0 && !qty.LessThan(min)
		if (err == nil) != want {
			t.Fatalf("qty %s min %s multiple %s: err %v, want pass=%v", qty, min, multiple, err, want)
		}
	})
}

func TestOperationTransition_Table(t *testing.T) {
	worked := Operation{OperNumber: 20, Status: OperFinished, HoursReported: decimal.RequireFromString("1.5")}
	cases := []struct {
		name    string
		order   Status
		op      Operation
		action  OperAction
		next    OperStatus
		changed bool
		code    string
	}{
		{"finish an open operation", Started, Operation{Status: OperOpen}, OperFinish, OperFinished, true, ""},
		{"finish a started operation", ReportedAsFinished, Operation{Status: OperStarted}, OperFinish, OperFinished, true, ""},
		{"finish again changes nothing", Started, Operation{Status: OperFinished}, OperFinish, OperFinished, false, ""},
		{"reopen with feedback is started", Started, worked, OperReopen, OperStarted, true, ""},
		{"reopen without feedback is open", Started, Operation{Status: OperFinished}, OperReopen, OperOpen, true, ""},
		{"reopen an open operation changes nothing", Started, Operation{Status: OperOpen}, OperReopen, OperOpen, false, ""},
		{"Released order takes no feedback", Released, Operation{Status: OperOpen}, OperFinish, "", false, CodeOperationState},
		{"Ended order is settled", Ended, worked, OperReopen, "", false, CodeOperationState},
		{"Created order", Created, Operation{Status: OperOpen}, OperFinish, "", false, CodeOperationState},
		{"subcontracted operation", Started, Operation{Status: OperOpen, Subcontract: true}, OperFinish, "", false, CodeOperationState},
		{"unknown command", Started, Operation{Status: OperOpen}, OperAction("split"), "", false, CodeInvalidInput},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			next, changed, err := OperationTransition(c.order, c.op, c.action)
			if c.code != "" {
				var de *Error
				if !errors.As(err, &de) || de.Code != c.code || changed {
					t.Fatalf("got (%v, %v), want code %s", changed, err, c.code)
				}
				return
			}
			if err != nil || next != c.next || changed != c.changed {
				t.Fatalf("got (%s, %v, %v), want (%s, %v)", next, changed, err, c.next, c.changed)
			}
		})
	}
}

// Finish then reopen returns an operation to the status its feedback gives it.
func TestOperationTransition_FinishReopenRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		hours := decimal.New(rapid.Int64Range(0, 3).Draw(t, "hours"), -2)
		good := decimal.New(rapid.Int64Range(0, 3).Draw(t, "good"), 0)
		status := OperOpen
		if !hours.IsZero() || !good.IsZero() {
			status = OperStarted
		}
		op := Operation{OperNumber: 10, Status: status, HoursReported: hours, QtyGood: good}
		finished, changed, err := OperationTransition(Started, op, OperFinish)
		if err != nil || !changed || finished != OperFinished {
			t.Fatalf("finish: %s %v %v", finished, changed, err)
		}
		op.Status = finished
		back, changed, err := OperationTransition(Started, op, OperReopen)
		if err != nil || !changed || back != status {
			t.Fatalf("reopen gave %s (%v, %v), want %s", back, changed, err, status)
		}
	})
}
