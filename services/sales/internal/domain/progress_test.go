package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func q(n int64) decimal.Decimal { return decimal.NewFromInt(n) }

func TestApplyProgress(t *testing.T) {
	l := LineProgress{Status: LineOpen, Quantity: q(10)}
	l, err := ApplyProgress(l, ProgressDelivered, q(4))
	if err != nil || l.Status != LinePicked || !l.Delivered.Equal(q(4)) {
		t.Fatalf("first partial delivery: %+v %v", l, err)
	}
	if _, err := ApplyProgress(l, ProgressDelivered, q(7)); err == nil {
		t.Fatal("over-delivery must be refused")
	}
	if _, err := ApplyProgress(l, ProgressInvoiced, q(5)); err == nil {
		t.Fatal("invoicing more than delivered must be refused")
	}
	inv, err := ApplyProgress(l, ProgressInvoiced, q(4))
	if err != nil || inv.Status != LinePicked || !inv.Invoiced.Equal(q(4)) {
		t.Fatalf("a partial invoice leaves the line open: %+v %v", inv, err)
	}
	l, err = ApplyProgress(inv, ProgressDelivered, q(6))
	if err != nil || l.Status != LineDelivered {
		t.Fatalf("whole quantity delivered: %+v %v", l, err)
	}
	l, err = ApplyProgress(l, ProgressInvoiced, q(6))
	if err != nil || l.Status != LineInvoiced {
		t.Fatalf("whole quantity invoiced: %+v %v", l, err)
	}
	for _, bad := range []struct {
		l    LineProgress
		kind string
		qty  decimal.Decimal
	}{
		{LineProgress{Status: LineCancelled, Quantity: q(1)}, ProgressDelivered, q(1)},
		{LineProgress{Status: LineOpen, Quantity: q(1)}, ProgressDelivered, q(0)},
		{LineProgress{Status: LineOpen, Quantity: q(1)}, "SHIPPED", q(1)},
	} {
		if _, err := ApplyProgress(bad.l, bad.kind, bad.qty); err == nil {
			t.Fatalf("must refuse %+v", bad)
		}
	}
}

func TestOrderStatusFor(t *testing.T) {
	open := LineProgress{Status: LineOpen, Quantity: q(5)}
	part := LineProgress{Status: LinePicked, Quantity: q(5), Delivered: q(2)}
	done := LineProgress{Status: LineDelivered, Quantity: q(5), Delivered: q(5)}
	inv := LineProgress{Status: LineInvoiced, Quantity: q(5), Delivered: q(5), Invoiced: q(5)}
	gone := LineProgress{Status: LineCancelled, Quantity: q(5)}
	for name, c := range map[string]struct {
		lines []LineProgress
		want  string
	}{
		"nothing delivered":      {[]LineProgress{open, open}, OrderConfirmed},
		"one line in part":       {[]LineProgress{open, part}, OrderPartiallyShipped},
		"one line done":          {[]LineProgress{open, done}, OrderPartiallyShipped},
		"all delivered":          {[]LineProgress{done, done}, OrderShipped},
		"cancelled line ignored": {[]LineProgress{done, gone}, OrderShipped},
		"delivered and invoiced": {[]LineProgress{done, inv}, OrderShipped},
		"all invoiced":           {[]LineProgress{inv, inv, gone}, OrderInvoiced},
	} {
		if got := OrderStatusFor(c.lines); got != c.want {
			t.Errorf("%s: got %s want %s", name, got, c.want)
		}
	}
}

func TestCancelRemainder(t *testing.T) {
	n := decimal.NewFromInt
	cases := []struct {
		name   string
		in     LineProgress
		status string
		qty    int64
		gave   int64
		err    bool
	}{
		{"undelivered open line is cancelled", LineProgress{Status: LineOpen, Quantity: n(10)}, LineCancelled, 10, 10, false},
		{"undelivered reserved line is cancelled", LineProgress{Status: LineReserved, Quantity: n(10)}, LineCancelled, 10, 10, false},
		{"partly delivered line is closed at the delivered quantity", LineProgress{Status: LinePicked, Quantity: n(10), Delivered: n(4)}, LineDelivered, 4, 6, false},
		{"delivered and invoiced part ends invoiced", LineProgress{Status: LinePicked, Quantity: n(10), Delivered: n(4), Invoiced: n(4)}, LineInvoiced, 4, 6, false},
		{"delivered in part, invoiced less", LineProgress{Status: LinePicked, Quantity: n(10), Delivered: n(4), Invoiced: n(1)}, LineDelivered, 4, 6, false},
		{"fully delivered line is left", LineProgress{Status: LineDelivered, Quantity: n(10), Delivered: n(10)}, LineDelivered, 10, 0, false},
		{"cancelled line is left", LineProgress{Status: LineCancelled, Quantity: n(10)}, LineCancelled, 10, 0, false},
		{"nothing delivered on a picked line is refused", LineProgress{Status: LinePicked, Quantity: n(10)}, LinePicked, 10, 0, true},
	}
	for _, c := range cases {
		got, gave, err := CancelRemainder(c.in)
		if (err != nil) != c.err {
			t.Fatalf("%s: err %v", c.name, err)
		}
		if got.Status != c.status || !got.Quantity.Equal(n(c.qty)) || !gave.Equal(n(c.gave)) {
			t.Fatalf("%s: got %s qty %s gave %s", c.name, got.Status, got.Quantity, gave)
		}
	}
}

func TestApplyProgressWithin(t *testing.T) {
	tol := DeliveryTolerance{OverdeliveryPct: decimal.RequireFromString("5"), UnderdeliveryPct: decimal.RequireFromString("2.5"), PartialDelivery: PartialAllowed}
	line := LineProgress{Status: LineOpen, Quantity: q(100)}

	// Over-delivery within 5 percent: the line follows what was shipped.
	got, outcome, err := ApplyProgressWithin(line, ProgressDelivered, q(105), tol, false)
	if err != nil || outcome != ToleranceOver || !got.Quantity.Equal(q(105)) || !got.Delivered.Equal(q(105)) || got.Status != LineDelivered {
		t.Fatalf("105 of 100 at 5%%: %+v %q %v", got, outcome, err)
	}
	if _, _, err := ApplyProgressWithin(line, ProgressDelivered, q(106), tol, false); err == nil {
		t.Fatal("106 of 100 at 5% must be refused")
	}
	// Over-delivery arrives as a second slip: 60 then 45.
	part, outcome, err := ApplyProgressWithin(line, ProgressDelivered, q(60), tol, false)
	if err != nil || outcome != "" || part.Status != LinePicked || !part.Quantity.Equal(q(100)) {
		t.Fatalf("first slip: %+v %q %v", part, outcome, err)
	}
	got, outcome, err = ApplyProgressWithin(part, ProgressDelivered, q(45), tol, false)
	if err != nil || outcome != ToleranceOver || !got.Quantity.Equal(q(105)) {
		t.Fatalf("second slip: %+v %q %v", got, outcome, err)
	}
	// A line delivered in full is not reopened by the tolerance: not when the
	// exact quantity was delivered, not once invoiced, not after an
	// over-delivery and not after its remainder was closed.
	for _, full := range []LineProgress{
		{Status: LineDelivered, Quantity: q(100), Delivered: q(100)},
		{Status: LineInvoiced, Quantity: q(100), Delivered: q(100), Invoiced: q(100)},
		{Status: LineDelivered, Quantity: q(103), Delivered: q(103)},
		{Status: LineDelivered, Quantity: q(98), Delivered: q(98)},
	} {
		if got, _, err := ApplyProgressWithin(full, ProgressDelivered, q(1), tol, false); err == nil {
			t.Fatalf("a line delivered in full took 1 more: %+v -> %+v", full, got)
		}
	}

	// Without final a short delivery is partial, even within the tolerance.
	got, outcome, err = ApplyProgressWithin(line, ProgressDelivered, q(98), tol, false)
	if err != nil || outcome != "" || got.Status != LinePicked || !got.Quantity.Equal(q(100)) {
		t.Fatalf("98 of 100 not final: %+v %q %v", got, outcome, err)
	}
	// A final delivery within 2.5 percent completes the line at the delivered quantity.
	got, outcome, err = ApplyProgressWithin(line, ProgressDelivered, q(98), tol, true)
	if err != nil || outcome != ToleranceUnder || !got.Quantity.Equal(q(98)) || got.Status != LineDelivered {
		t.Fatalf("98 of 100 final at 2.5%%: %+v %q %v", got, outcome, err)
	}
	// 97.5 is exactly the lower bound; 97 is below it and a final 97 is refused.
	got, outcome, err = ApplyProgressWithin(line, ProgressDelivered, decimal.RequireFromString("97.5"), tol, true)
	if err != nil || outcome != ToleranceUnder {
		t.Fatalf("97.5 is within the tolerance: %+v %q %v", got, outcome, err)
	}
	if got, _, err := ApplyProgressWithin(line, ProgressDelivered, q(97), tol, true); err == nil {
		t.Fatalf("a final 97 of 100 at 2.5%% must be refused: %+v", got)
	}
	// Final with no under-delivery tolerance only completes an exact delivery.
	if _, _, err := ApplyProgressWithin(line, ProgressDelivered, q(99), DefaultTolerance(), true); err == nil {
		t.Fatal("a final 99 of 100 at 0% must be refused")
	}
	if got, outcome, err := ApplyProgressWithin(line, ProgressDelivered, q(100), DefaultTolerance(), true); err != nil || outcome != "" || got.Status != LineDelivered {
		t.Fatalf("a final exact delivery: %+v %q %v", got, outcome, err)
	}
	// A delivery to the ordered quantity is no tolerance case.
	got, outcome, err = ApplyProgressWithin(LineProgress{Status: LinePicked, Quantity: q(100), Delivered: q(98), Invoiced: q(98)}, ProgressDelivered, q(2), tol, false)
	if err != nil || outcome != "" || got.Status != LineDelivered {
		t.Fatalf("exact: %+v %q %v", got, outcome, err)
	}
	got, outcome, err = ApplyProgressWithin(LineProgress{Status: LinePicked, Quantity: q(100), Delivered: q(50), Invoiced: q(50)}, ProgressDelivered, q(48), tol, true)
	if err != nil || outcome != ToleranceUnder || got.Status != LineDelivered || !got.Invoiced.Equal(q(50)) {
		t.Fatalf("under after a partial invoice: %+v %q %v", got, outcome, err)
	}

	// The default tolerance changes nothing, an invoice is never adjusted.
	if _, _, err := ApplyProgressWithin(line, ProgressDelivered, q(101), DefaultTolerance(), false); err == nil {
		t.Fatal("101 of 100 at 0% must be refused")
	}
	got, outcome, err = ApplyProgressWithin(line, ProgressDelivered, q(99), DefaultTolerance(), false)
	if err != nil || outcome != "" || got.Status != LinePicked {
		t.Fatalf("99 of 100 at 0%%: %+v %q %v", got, outcome, err)
	}
	if _, _, err := ApplyProgressWithin(LineProgress{Status: LineDelivered, Quantity: q(100), Delivered: q(100)}, ProgressInvoiced, q(101), tol, false); err == nil {
		t.Fatal("an invoice never passes the delivered quantity")
	}
	if _, _, err := ApplyProgressWithin(LineProgress{Status: LineCancelled, Quantity: q(100)}, ProgressDelivered, q(1), tol, true); err == nil {
		t.Fatal("a cancelled line takes nothing")
	}
}
