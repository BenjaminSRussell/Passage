package domain

import (
	"reflect"
	"testing"

	"github.com/shopspring/decimal"
)

func vo(num, item, typ, qty, od, dd string) VersionOrder {
	return VersionOrder{Number: num, ItemID: item, OrderType: typ, Qty: decimal.RequireFromString(qty), OrderDate: od, DeliveryDate: dd}
}

func numbers(os []VersionOrder) []string {
	out := []string{}
	for _, o := range os {
		out = append(out, o.Number)
	}
	return out
}

func TestDiffVersions_AddedRemovedChanged(t *testing.T) {
	from := []VersionOrder{
		vo("PO-1", "a", "purchase", "100", "2026-01-01", "2026-01-08"),  // unchanged
		vo("PO-2", "a", "purchase", "50", "2026-01-10", "2026-01-17"),   // qty changes
		vo("PO-3", "b", "production", "20", "2026-01-02", "2026-01-05"), // removed
	}
	to := []VersionOrder{
		vo("PO-1", "a", "purchase", "100", "2026-01-01", "2026-01-08"),
		vo("PO-9", "a", "purchase", "70", "2026-01-10", "2026-01-17"),
		vo("PO-4", "c", "transfer", "5", "2026-01-03", "2026-01-04"), // added
	}
	d := DiffVersions(from, to)
	if d.Unchanged != 1 || len(d.Changed) != 1 || len(d.Added) != 1 || len(d.Removed) != 1 {
		t.Fatalf("unexpected diff: %+v", d)
	}
	if c := d.Changed[0]; c.From.Number != "PO-2" || c.To.Number != "PO-9" || !c.QtyDelta.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("changed = %+v", c)
	}
	if d.Removed[0].Number != "PO-3" || d.Added[0].Number != "PO-4" {
		t.Fatalf("removed %v added %v", numbers(d.Removed), numbers(d.Added))
	}
	if !d.QtyDelta["purchase"].Equal(decimal.NewFromInt(20)) || !d.QtyDelta["production"].Equal(decimal.NewFromInt(-20)) || !d.QtyDelta["transfer"].Equal(decimal.NewFromInt(5)) {
		t.Fatalf("per-type delta = %v", d.QtyDelta)
	}
}

func TestDiffVersions_IdenticalAndDateOnlyChange(t *testing.T) {
	a := []VersionOrder{vo("1", "a", "purchase", "10", "2026-01-01", "2026-01-08")}
	if d := DiffVersions(a, a); d.Unchanged != 1 || len(d.Changed)+len(d.Added)+len(d.Removed) != 0 || len(d.QtyDelta) != 0 {
		t.Fatalf("identical versions differ: %+v", d)
	}
	b := []VersionOrder{vo("7", "a", "purchase", "10", "2026-01-02", "2026-01-08")}
	d := DiffVersions(a, b)
	if len(d.Changed) != 1 || !d.Changed[0].QtyDelta.IsZero() || d.Unchanged != 0 {
		t.Fatalf("order-date change not reported: %+v", d)
	}
}

// A vendor switch (for example a new quota split) is a change even when the
// quantity and dates stay the same.
func TestDiffVersions_VendorChange(t *testing.T) {
	a := vo("1", "a", "purchase", "10", "2026-01-01", "2026-01-08")
	b := a
	a.VendorID, b.VendorID = "v1", "v2"
	d := DiffVersions([]VersionOrder{a}, []VersionOrder{b})
	if len(d.Changed) != 1 || d.Changed[0].From.VendorID != "v1" || d.Changed[0].To.VendorID != "v2" || d.Unchanged != 0 {
		t.Fatalf("vendor change not reported: %+v", d)
	}
}

// Orders under one key that are equal in both versions pair first, so a new
// order next to an unchanged one is an addition, not a change plus a removal.
func TestDiffVersions_ExactMatchesPairFirst(t *testing.T) {
	from := []VersionOrder{vo("1", "a", "purchase", "10", "d", "x"), vo("2", "a", "purchase", "30", "d", "x")}
	to := []VersionOrder{vo("5", "a", "purchase", "30", "d", "x")}
	d := DiffVersions(from, to)
	if d.Unchanged != 1 || len(d.Changed) != 0 || len(d.Removed) != 1 || d.Removed[0].Number != "1" || !d.QtyDelta["purchase"].Equal(decimal.NewFromInt(-10)) {
		t.Fatalf("unexpected: %+v", d)
	}

	from = []VersionOrder{vo("1", "a", "purchase", "10", "d", "x"), vo("2", "a", "purchase", "20", "d", "x"), vo("3", "a", "purchase", "40", "d", "x")}
	to = []VersionOrder{vo("7", "a", "purchase", "20", "d", "x"), vo("8", "a", "purchase", "15", "d", "x")}
	d = DiffVersions(from, to)
	// 20 = 20 is unchanged; the leftovers 10 and 40 against 15 pair in quantity order.
	if d.Unchanged != 1 || len(d.Changed) != 1 || d.Changed[0].From.Number != "1" || d.Changed[0].To.Number != "8" ||
		len(d.Removed) != 1 || d.Removed[0].Number != "3" || !d.QtyDelta["purchase"].Equal(decimal.NewFromInt(-35)) {
		t.Fatalf("unexpected: %+v", d)
	}
}

func TestDiffVersions_DoesNotDependOnInputOrder(t *testing.T) {
	from := []VersionOrder{
		vo("1", "b", "purchase", "1", "d", "y"), vo("2", "a", "purchase", "2", "d", "x"),
		vo("3", "a", "purchase", "5", "d", "x"), vo("4", "a", "production", "9", "d", "x"),
	}
	to := []VersionOrder{vo("8", "a", "purchase", "7", "d", "x"), vo("9", "c", "transfer", "3", "d", "z")}
	reverse := func(in []VersionOrder) []VersionOrder {
		out := make([]VersionOrder, len(in))
		for i, o := range in {
			out[len(in)-1-i] = o
		}
		return out
	}
	a, b := DiffVersions(from, to), DiffVersions(reverse(from), reverse(to))
	if !reflect.DeepEqual(numbers(a.Added), numbers(b.Added)) || !reflect.DeepEqual(numbers(a.Removed), numbers(b.Removed)) || len(a.Changed) != len(b.Changed) {
		t.Fatalf("result depends on input order: %+v vs %+v", a, b)
	}
	for i := range a.Changed {
		if a.Changed[i].From.Number != b.Changed[i].From.Number || a.Changed[i].To.Number != b.Changed[i].To.Number {
			t.Fatalf("changed pairs differ: %+v vs %+v", a.Changed, b.Changed)
		}
	}
	if got := numbers(a.Removed); !reflect.DeepEqual(got, []string{"4", "3", "1"}) {
		t.Fatalf("removed not in item, type, delivery order: %v", got)
	}
}

// Orders of one item, type and delivery day in two planning scopes never pair up
// (FR-22.18): moving the order from the site to a warehouse is a removal and an addition.
func TestDiffVersions_ScopesDoNotMatch(t *testing.T) {
	site := vo("1", "a", "purchase", "10", "2026-01-01", "2026-01-08")
	wh := vo("1", "a", "purchase", "10", "2026-01-01", "2026-01-08")
	wh.WarehouseID = "w"
	d := DiffVersions([]VersionOrder{site}, []VersionOrder{wh})
	if len(d.Added) != 1 || len(d.Removed) != 1 || len(d.Changed) != 0 || d.Unchanged != 0 {
		t.Fatalf("diff = %+v", d)
	}
}
