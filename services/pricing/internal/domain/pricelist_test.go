package domain_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
)

func TestPriceListFor_KeepsApplicableScopesMostSpecificFirst(t *testing.T) {
	recs := []domain.Record{
		rec("all", domain.TypePrice, domain.CustomerKindAll, "", domain.ProductKindProduct, "P1", "USD", "0", "100"),
		rec("grp", domain.TypePrice, domain.CustomerKindGroup, "PG-A", domain.ProductKindProduct, "P1", "USD", "0", "95"),
		rec("cust", domain.TypePrice, domain.CustomerKindCustomer, "C1", domain.ProductKindProduct, "P1", "USD", "0", "90"),
		rec("other", domain.TypePrice, domain.CustomerKindCustomer, "C2", domain.ProductKindProduct, "P1", "USD", "0", "50"),
		rec("othergrp", domain.TypePrice, domain.CustomerKindGroup, "PG-B", domain.ProductKindProduct, "P1", "USD", "0", "60"),
		rec("p0", domain.TypePrice, domain.CustomerKindAll, "", domain.ProductKindProduct, "P0", "USD", "0", "10"),
		rec("brk", domain.TypePrice, domain.CustomerKindCustomer, "C1", domain.ProductKindProduct, "P1", "USD", "10", "85"),
		rec("disc", domain.TypeLineDisc, domain.CustomerKindAll, "", domain.ProductKindProduct, "P1", "USD", "0", "0"),
	}
	got := domain.PriceListFor(recs, "C1", "PG-A", map[domain.ConditionType]bool{domain.TypePrice: true})
	var ids, scopes []string
	for _, e := range got {
		ids, scopes = append(ids, e.Record.ID), append(scopes, e.Scope)
	}
	want := []string{"p0", "cust", "brk", "grp", "all"}
	if len(ids) != len(want) {
		t.Fatalf("price list = %v; want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("price list = %v; want %v", ids, want)
		}
	}
	if scopes[1] != domain.ScopeCustomer || scopes[3] != domain.ScopeGroup || scopes[4] != domain.ScopeAll {
		t.Fatalf("scopes = %v", scopes)
	}
}

func TestPriceListFor_NoGroupDropsGroupRecordsAndNoTypesKeepsAll(t *testing.T) {
	recs := []domain.Record{
		rec("grp", domain.TypePrice, domain.CustomerKindGroup, "PG-A", domain.ProductKindProduct, "P1", "USD", "0", "95"),
		rec("disc", domain.TypeLineDisc, domain.CustomerKindAll, "", domain.ProductKindProduct, "P1", "USD", "0", "0"),
	}
	got := domain.PriceListFor(recs, "C1", "", nil)
	if len(got) != 1 || got[0].Record.ID != "disc" {
		t.Fatalf("without a customer group only the all-customer record applies: %+v", got)
	}
}

var lim = domain.NetPriceLimits{Rows: 500, Breaks: 2000}

func TestNetPriceList_NetPricePerBreakAndWinner(t *testing.T) {
	recs := []domain.Record{
		rec("all", domain.TypePrice, domain.CustomerKindAll, "", domain.ProductKindProduct, "P1", "USD", "0", "100"),
		rec("cust10", domain.TypePrice, domain.CustomerKindCustomer, "C1", domain.ProductKindProduct, "P1", "USD", "10", "85"),
		rec("p2", domain.TypePrice, domain.CustomerKindAll, "", domain.ProductKindProduct, "P2", "USD", "5", "7"),
		rec("grp", domain.TypePrice, domain.CustomerKindAll, "", domain.ProductKindGroup, "G9", "USD", "0", "3"),
	}
	for i := range recs {
		recs[i].Unit = "EA"
	}
	disc := rec("d50", domain.TypeLineDisc, domain.CustomerKindAll, "", domain.ProductKindProduct, "P1", "USD", "50", "0")
	disc.Unit, disc.DiscPct1 = "EA", d("10")
	recs = append(recs, disc)
	entries := domain.PriceListFor(recs, "C1", "PG-A", nil)
	rows, truncated := domain.NetPriceList(domain.Input{Header: header("USD"), Records: recs}, entries, domain.NetPriceFilter{}, lim)
	if len(rows) != 3 || truncated {
		t.Fatalf("rows = %+v (truncated %v); want P1, P2 and group G9", rows, truncated)
	}
	byKey := map[string]domain.NetPriceRow{}
	for _, r := range rows {
		byKey[r.ProductKey] = r
	}
	p1, p2, g := byKey["P1"], byKey["P2"], byKey["G9"]
	if len(p1.Breaks) != 3 {
		t.Fatalf("P1 row = %+v; want breaks 1, 10, 50", p1)
	}
	want := []struct{ qty, net, rec string }{{"1", "100", "all"}, {"10", "85", "cust10"}, {"50", "76.5", "cust10"}}
	for i, w := range want {
		b := p1.Breaks[i]
		if !b.Quantity.Equal(d(w.qty)) || !b.NetUnitPrice.Equal(d(w.net)) || b.PriceRecordID != w.rec {
			t.Fatalf("P1 break %d = qty %s net %s via %s; want %+v", i, b.Quantity, b.NetUnitPrice, b.PriceRecordID, w)
		}
	}
	if !p1.Breaks[2].LineNet.Equal(d("3825")) {
		t.Fatalf("line net at 50 = %s; want 3825", p1.Breaks[2].LineNet)
	}
	// P2's only price starts at 5, so there is no break (and no error row) below it.
	if len(p2.Breaks) != 1 || !p2.Breaks[0].Quantity.Equal(d("5")) || p2.Breaks[0].ErrorCode != "" {
		t.Fatalf("P2 row = %+v; want one break at 5", p2)
	}
	if g.ProductKind != domain.ProductKindGroup || len(g.Breaks) != 1 || !g.Breaks[0].NetUnitPrice.Equal(d("3")) {
		t.Fatalf("group row = %+v; want 3 for a line of the group", g)
	}
}

// Every break is the line of a one-line Resolve of that quantity under the same header, records and
// procedure: the batch must never show a price a sales line would not get.
func TestNetPriceList_EachBreakEqualsAOneLineResolve(t *testing.T) {
	recs := []domain.Record{
		rec("base", domain.TypePrice, domain.CustomerKindAll, "", domain.ProductKindProduct, "P1", "USD", "0", "100"),
		rec("vol", domain.TypePrice, domain.CustomerKindGroup, "PG-A", domain.ProductKindProduct, "P1", "USD", "25", "92"),
		rec("ld-any", domain.TypeLineDisc, domain.CustomerKindAll, "", domain.ProductKindAll, "", "USD", "40", "0"),
		rec("ld-cust", domain.TypeLineDisc, domain.CustomerKindCustomer, "C1", domain.ProductKindProduct, "P1", "USD", "12", "5"),
		rec("ml", domain.TypeMultiline, domain.CustomerKindAll, "", domain.ProductKindGroup, "G9", "USD", "30", "0"),
		rec("g-price", domain.TypePrice, domain.CustomerKindAll, "", domain.ProductKindGroup, "G9", "USD", "0", "20"),
		rec("v-price", domain.TypePrice, domain.CustomerKindCustomer, "C1", domain.ProductKindVariant, "P1", "USD", "0", "110"),
		rec("total", domain.TypeTotalDisc, domain.CustomerKindAll, "", domain.ProductKindAll, "", "USD", "0", "0"),
	}
	recs[2].DiscPct1 = d("10")
	recs[4].DiscPct1 = d("15")
	recs[6].Variant = "RED"
	recs[7].DiscPct1 = d("50")
	for i := range recs {
		if recs[i].Type != domain.TypeTotalDisc && recs[i].ID != "ld-any" {
			recs[i].Unit = "EA"
		}
	}
	proc := &domain.Procedure{ID: "proc", Name: "STD", Version: 1, Steps: []domain.ProcedureStep{
		{StepNo: 10, ConditionType: domain.TypePrice},
		{StepNo: 20, ConditionType: domain.TypeLineDisc, ExclusionGroup: "X", ExclusionMode: domain.ExclusionBest},
		{StepNo: 30, ConditionType: domain.TypeMultiline, ExclusionGroup: "X", ExclusionMode: domain.ExclusionBest},
		{StepNo: 40, ConditionType: domain.TypeTotalDisc},
	}}
	in := domain.Input{Header: header("USD"), Set: domain.NewRecordSet(recs), Procedure: proc}
	rows, _ := domain.NetPriceList(in, domain.PriceListFor(recs, "C1", "PG-A", nil), domain.NetPriceFilter{}, lim)
	if len(rows) != 3 {
		t.Fatalf("rows = %+v; want P1, P1/RED and G9", rows)
	}
	breaks := 0
	for _, row := range rows {
		for _, b := range row.Breaks {
			breaks++
			l := domain.Line{No: 1, Variant: row.Variant, Unit: row.Unit, Quantity: b.Quantity}
			if row.ProductKind == domain.ProductKindGroup {
				l.ProductGroupID = row.ProductKey
			} else {
				l.ProductID = row.ProductKey
			}
			one := in
			one.Lines = []domain.Line{l}
			lr := domain.Resolve(one).Lines[0]
			if !b.UnitPrice.Equal(lr.UnitPrice) || !b.NetUnitPrice.Equal(lr.NetUnit) || !b.LineNet.Equal(lr.LineNet) ||
				!reflect.DeepEqual(b.Applied, lr.Applied) || b.ErrorCode != lr.Error {
				t.Fatalf("%s/%s at %s = %+v; a one-line resolve gives %+v", row.ProductKey, row.Variant, b.Quantity, b, lr)
			}
		}
	}
	// The any-unit, all-products discount at 40 is a break of every row; P1 also breaks at 12 and 25.
	var p1 domain.NetPriceRow
	for _, row := range rows {
		if row.ProductKey == "P1" && row.Variant == "" {
			p1 = row
		}
	}
	if got := fmt.Sprint(p1.Breaks); breaks < 8 || len(p1.Breaks) != 4 || !p1.Breaks[1].Quantity.Equal(d("12")) || !p1.Breaks[3].Quantity.Equal(d("40")) {
		t.Fatalf("P1 breaks = %s; want 1, 12, 25, 40", got)
	}
	// The order-level total discount never reaches a break.
	for _, ap := range p1.Breaks[3].Applied {
		if ap.Type == domain.TypeTotalDisc {
			t.Fatalf("break carries the order-level %s", ap.RecordID)
		}
	}
}

func TestNetPriceList_CapsBreaksAndSkipsDiscountOnlyRows(t *testing.T) {
	var recs []domain.Record
	for i := 1; i <= domain.MaxNetPriceBreaks+5; i++ {
		r := rec(fmt.Sprintf("r%02d", i), domain.TypePrice, domain.CustomerKindAll, "", domain.ProductKindProduct, "P1", "USD", fmt.Sprint(i), "10")
		r.Unit = "EA"
		recs = append(recs, r)
	}
	recs = append(recs, rec("lonely", domain.TypeLineDisc, domain.CustomerKindAll, "", domain.ProductKindProduct, "P7", "USD", "1", "0"))
	rows, _ := domain.NetPriceList(domain.Input{Header: header("USD"), Records: recs}, domain.PriceListFor(recs, "C1", "", nil), domain.NetPriceFilter{}, lim)
	if len(rows) != 1 || len(rows[0].Breaks) != domain.MaxNetPriceBreaks || !rows[0].Breaks[domain.MaxNetPriceBreaks-1].Quantity.Equal(d("20")) {
		t.Fatalf("rows = %+v; want one row with the lowest %d breaks", rows, domain.MaxNetPriceBreaks)
	}
}

func TestNetPriceList_RowCapAndFilterPickRowsBeforeResolving(t *testing.T) {
	recs := []domain.Record{rec("ld", domain.TypeLineDisc, domain.CustomerKindAll, "", domain.ProductKindAll, "", "USD", "100", "0")}
	recs[0].DiscPct1 = d("20")
	for i := 1; i <= 5; i++ {
		r := rec(fmt.Sprintf("p%d", i), domain.TypePrice, domain.CustomerKindAll, "", domain.ProductKindProduct, fmt.Sprintf("P%d", i), "USD", "0", "10")
		r.Unit = "EA"
		recs = append(recs, r)
	}
	g := rec("g", domain.TypePrice, domain.CustomerKindAll, "", domain.ProductKindGroup, "P3", "USD", "0", "4")
	g.Unit = "EA"
	recs = append(recs, g)
	entries := domain.PriceListFor(recs, "C1", "", nil)
	in := domain.Input{Header: header("USD"), Records: recs}
	rows, truncated := domain.NetPriceList(in, entries, domain.NetPriceFilter{}, domain.NetPriceLimits{Rows: 2, Breaks: 100})
	if len(rows) != 2 || !truncated || rows[0].ProductKey != "P1" || rows[1].ProductKey != "P2" {
		t.Fatalf("capped rows = %+v (truncated %v); want P1, P2 and truncated", rows, truncated)
	}
	if rows, truncated = domain.NetPriceList(in, entries, domain.NetPriceFilter{}, domain.NetPriceLimits{Rows: 6, Breaks: 100}); len(rows) != 6 || truncated {
		t.Fatalf("a list at the cap = %d rows, truncated %v; want 6, false", len(rows), truncated)
	}
	// Each product row has the breaks 1 and 100: a budget of 5 breaks takes two rows, never half a row.
	if rows, truncated = domain.NetPriceList(in, entries, domain.NetPriceFilter{}, domain.NetPriceLimits{Rows: 500, Breaks: 5}); len(rows) != 2 || !truncated || len(rows[1].Breaks) != 2 {
		t.Fatalf("break budget = %+v (truncated %v); want two whole rows and truncated", rows, truncated)
	}
	rows, truncated = domain.NetPriceList(in, entries, domain.NetPriceFilter{ProductID: "P3"}, domain.NetPriceLimits{Rows: 1, Breaks: 2})
	if len(rows) != 1 || truncated || rows[0].ProductKind != domain.ProductKindProduct || len(rows[0].Breaks) != 2 || !rows[0].Breaks[1].NetUnitPrice.Equal(d("8")) {
		t.Fatalf("productId P3 = %+v (truncated %v); want the product row only, with the all-products break at 100", rows, truncated)
	}
	rows, _ = domain.NetPriceList(in, entries, domain.NetPriceFilter{ProductID: "P1", ProductGroupID: "P3"}, lim)
	if len(rows) != 2 || rows[0].ProductKey != "P1" || rows[1].ProductKind != domain.ProductKindGroup {
		t.Fatalf("product and group filter = %+v; want P1 and the group P3", rows)
	}
	if rows, _ = domain.NetPriceList(in, entries, domain.NetPriceFilter{ProductID: "NOPE"}, lim); rows == nil || len(rows) != 0 {
		t.Fatalf("unknown product = %#v; want an empty, non-nil list", rows)
	}
}

// BenchmarkNetPriceList_FullPage prices the most one request may: 2,000 breaks (100 product rows of
// 20 breaks, one-line resolves) against 10,500 records, the bound api.MaxNetPriceBreaks sets.
// Run: go test ./internal/domain -bench NetPriceList -run '^$'
func BenchmarkNetPriceList_FullPage(b *testing.B) {
	var recs []domain.Record
	for p := 0; p < 500; p++ {
		for q := 0; q < 20; q++ {
			r := rec(fmt.Sprintf("r%03d-%02d", p, q), domain.TypePrice, domain.CustomerKindAll, "", domain.ProductKindProduct, fmt.Sprintf("P%03d", p), "USD", fmt.Sprint(q*10), "10")
			r.Unit = "EA"
			recs = append(recs, r)
		}
		c := rec(fmt.Sprintf("c%03d", p), domain.TypePrice, domain.CustomerKindCustomer, "C1", domain.ProductKindProduct, fmt.Sprintf("P%03d", p), "USD", "0", "9")
		c.Unit = "EA"
		recs = append(recs, c)
	}
	in := domain.Input{Header: header("USD"), Set: domain.NewRecordSet(recs)}
	entries := domain.PriceListFor(recs, "C1", "PG-A", nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, truncated := domain.NetPriceList(in, entries, domain.NetPriceFilter{}, lim)
		if len(rows) != 100 || len(rows[0].Breaks) != 20 || !truncated {
			b.Fatalf("rows = %d", len(rows))
		}
	}
}

// The price list filter keeps the records keyed to the product (its variants too) or the group,
// in their order, and leaves out the all-products records and the other products.
func TestNetPriceFilterEntries_KeepsProductVariantAndGroupRecords(t *testing.T) {
	variant := rec("var", domain.TypePrice, domain.CustomerKindAll, "", domain.ProductKindVariant, "P1", "USD", "0", "99")
	variant.Variant = "RED"
	recs := []domain.Record{
		rec("p1", domain.TypePrice, domain.CustomerKindAll, "", domain.ProductKindProduct, "P1", "USD", "0", "100"),
		variant,
		rec("p2", domain.TypePrice, domain.CustomerKindAll, "", domain.ProductKindProduct, "P2", "USD", "0", "50"),
		rec("g9", domain.TypeLineDisc, domain.CustomerKindAll, "", domain.ProductKindGroup, "G9", "USD", "0", "0"),
		rec("any", domain.TypeLineDisc, domain.CustomerKindAll, "", domain.ProductKindAll, "", "USD", "0", "0"),
	}
	entries := domain.PriceListFor(recs, "C1", "", nil)
	ids := func(f domain.NetPriceFilter) []string {
		var out []string
		for _, e := range f.Entries(entries) {
			out = append(out, e.Record.ID)
		}
		return out
	}
	for _, c := range []struct {
		f    domain.NetPriceFilter
		want []string
	}{
		{domain.NetPriceFilter{}, []string{"any", "g9", "p1", "var", "p2"}},
		{domain.NetPriceFilter{ProductID: "P1"}, []string{"p1", "var"}},
		{domain.NetPriceFilter{ProductGroupID: "G9"}, []string{"g9"}},
		{domain.NetPriceFilter{ProductID: "P2", ProductGroupID: "G9"}, []string{"g9", "p2"}},
		{domain.NetPriceFilter{ProductID: "P3"}, nil},
	} {
		if got := ids(c.f); !reflect.DeepEqual(got, c.want) {
			t.Errorf("filter %+v = %v; want %v", c.f, got, c.want)
		}
	}
}
