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
