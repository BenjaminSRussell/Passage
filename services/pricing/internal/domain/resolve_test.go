package domain_test

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
)

var (
	pricingDate = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	validFrom   = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	validTo     = time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func rec(id string, t domain.ConditionType, ck domain.CustomerKind, ckey string, pk domain.ProductKind, pkey, currency, from, amount string) domain.Record {
	return domain.Record{ID: id, Status: domain.StatusActive, Type: t, CustomerKind: ck, CustomerKey: ckey, ProductKind: pk, ProductKey: pkey,
		Currency: currency, FromQuantity: d(from), Amount: d(amount), ValidFrom: validFrom, ValidTo: validTo}
}

func header(currency string) domain.Header {
	return domain.Header{CustomerID: "C1", CustomerGroupID: "PG-A", Currency: currency, PricingDate: pricingDate}
}

func line(qty string) domain.Line {
	return domain.Line{No: 1, ProductID: "P1", ProductGroupID: "G1", Unit: "EA", Quantity: d(qty)}
}

func TestResolve_AccessSequenceFirstHitStops(t *testing.T) {
	// AC-26.1: access 2 (customer x product) wins at 95.00; access 4 is not evaluated.
	recs := []domain.Record{
		rec("r2", domain.TypePrice, domain.CustomerKindCustomer, "C1", domain.ProductKindProduct, "P1", "USD", "0", "95.00"),
		rec("r4", domain.TypePrice, domain.CustomerKindGroup, "PG-A", domain.ProductKindProduct, "P1", "USD", "0", "90.00"),
	}
	res := domain.Resolve(domain.Input{Header: header("USD"), Lines: []domain.Line{line("1")}, Records: recs})
	lr := res.Lines[0]
	if !lr.UnitPrice.Equal(d("95")) || lr.Applied[0].RecordID != "r2" {
		t.Fatalf("price = %s via %+v; want 95 via r2", lr.UnitPrice, lr.Applied)
	}
	if access := lr.Trace[0].Accesses[3]; access.Evaluated || access.Reason == "" {
		t.Fatalf("access 4 must be shown as not evaluated, got %+v", access)
	}
}

func TestResolve_SearchNextKeepsLowerNet(t *testing.T) {
	// AC-26.2: search_next on access 2 continues to access 4 and keeps 90.00.
	r2 := rec("r2", domain.TypePrice, domain.CustomerKindCustomer, "C1", domain.ProductKindProduct, "P1", "USD", "0", "95.00")
	r2.SearchNext = true
	recs := []domain.Record{r2, rec("r4", domain.TypePrice, domain.CustomerKindGroup, "PG-A", domain.ProductKindProduct, "P1", "USD", "0", "90.00")}
	res := domain.Resolve(domain.Input{Header: header("USD"), Lines: []domain.Line{line("1")}, Records: recs})
	lr := res.Lines[0]
	if !lr.UnitPrice.Equal(d("90")) || lr.Applied[0].RecordID != "r4" {
		t.Fatalf("price = %s via %+v; want 90 via r4", lr.UnitPrice, lr.Applied)
	}
	if !lr.Trace[0].Accesses[3].Evaluated {
		t.Fatal("access 4 must be evaluated under search_next")
	}
}

func TestResolve_QuantityBreaks(t *testing.T) {
	// AC-26.3: breaks 0 -> 10.00, 100 -> 9.50, 500 -> 9.00 EUR.
	recs := []domain.Record{
		rec("b0", domain.TypePrice, domain.CustomerKindCustomer, "C1", domain.ProductKindProduct, "P1", "EUR", "0", "10.00"),
		rec("b100", domain.TypePrice, domain.CustomerKindCustomer, "C1", domain.ProductKindProduct, "P1", "EUR", "100", "9.50"),
		rec("b500", domain.TypePrice, domain.CustomerKindCustomer, "C1", domain.ProductKindProduct, "P1", "EUR", "500", "9.00"),
	}
	cases := []struct{ qty, want string }{{"500", "9"}, {"499", "9.5"}, {"99", "10"}}
	for _, c := range cases {
		res := domain.Resolve(domain.Input{Header: header("EUR"), Lines: []domain.Line{line(c.qty)}, Records: recs})
		if got := res.Lines[0].UnitPrice; !got.Equal(d(c.want)) {
			t.Fatalf("qty %s: unit price = %s; want %s", c.qty, got, c.want)
		}
	}
}

func TestResolve_TwoPercentDiscountsAreSequential(t *testing.T) {
	// AC-26.5: 10% then 5% on 100.00 x 3 = 256.50.
	price := rec("p", domain.TypePrice, domain.CustomerKindCustomer, "C1", domain.ProductKindProduct, "P1", "USD", "0", "100.00")
	disc := rec("d", domain.TypeLineDisc, domain.CustomerKindAll, "", domain.ProductKindAll, "", "USD", "0", "0")
	disc.DiscPct1, disc.DiscPct2 = d("10"), d("5")
	res := domain.Resolve(domain.Input{Header: header("USD"), Lines: []domain.Line{line("3")}, Records: []domain.Record{price, disc}})
	if lr := res.Lines[0]; !lr.LineNet.Equal(d("256.50")) || !lr.NetUnit.Equal(d("85.5")) {
		t.Fatalf("line net = %s, net unit = %s; want 256.50 and 85.5", lr.LineNet, lr.NetUnit)
	}
}

func TestResolve_TotalDiscountRoundsOnce(t *testing.T) {
	price := rec("p", domain.TypePrice, domain.CustomerKindCustomer, "C1", domain.ProductKindProduct, "P1", "USD", "0", "10.00")
	total := rec("t", domain.TypeTotalDisc, domain.CustomerKindCustomer, "C1", domain.ProductKindAll, "", "USD", "0", "0")
	total.DiscPct1 = d("15")
	res := domain.Resolve(domain.Input{Header: header("USD"), Lines: []domain.Line{line("1"), line("2")}, Records: []domain.Record{price, total}})
	if !res.Subtotal.Equal(d("30")) || !res.OrderNet.Equal(d("25.50")) || !res.TotalDiscount.Equal(d("4.50")) {
		t.Fatalf("subtotal %s net %s discount %s; want 30, 25.50, 4.50", res.Subtotal, res.OrderNet, res.TotalDiscount)
	}
}

func TestResolve_MissingPriceIsALineError(t *testing.T) {
	res := domain.Resolve(domain.Input{Header: header("USD"), Lines: []domain.Line{line("1")}})
	if lr := res.Lines[0]; lr.Error != domain.CodePriceNotFound || !lr.LineNet.IsZero() {
		t.Fatalf("want %s on the line, got %+v", domain.CodePriceNotFound, lr)
	}
}

func TestResolve_NoCrossCurrencyFallback(t *testing.T) {
	recs := []domain.Record{rec("p", domain.TypePrice, domain.CustomerKindCustomer, "C1", domain.ProductKindProduct, "P1", "EUR", "0", "10")}
	if res := domain.Resolve(domain.Input{Header: header("USD"), Lines: []domain.Line{line("1")}, Records: recs}); res.Lines[0].Error == "" {
		t.Fatal("an EUR record must not price a USD document")
	}
}

func TestResolve_InactiveAndOutOfValidityIgnored(t *testing.T) {
	draft := rec("d", domain.TypePrice, domain.CustomerKindCustomer, "C1", domain.ProductKindProduct, "P1", "USD", "0", "5")
	draft.Status = domain.StatusDraft
	expired := rec("e", domain.TypePrice, domain.CustomerKindCustomer, "C1", domain.ProductKindProduct, "P1", "USD", "0", "5")
	expired.ValidTo = time.Date(2026, 5, 31, 0, 0, 0, 0, time.UTC)
	res := domain.Resolve(domain.Input{Header: header("USD"), Lines: []domain.Line{line("1")}, Records: []domain.Record{draft, expired}})
	if res.Lines[0].Error != domain.CodePriceNotFound {
		t.Fatalf("draft and expired records must not price, got %+v", res.Lines[0])
	}
}

func TestResolve_ExplainHasStableReference(t *testing.T) {
	recs := []domain.Record{rec("r2", domain.TypePrice, domain.CustomerKindCustomer, "C1", domain.ProductKindProduct, "P1", "USD", "0", "95")}
	in := domain.Input{Header: header("USD"), Lines: []domain.Line{line("1")}, Records: recs}
	a, refA, err := domain.ExplainTrace(domain.Resolve(in))
	if err != nil {
		t.Fatal(err)
	}
	b, refB, _ := domain.ExplainTrace(domain.Resolve(in))
	if refA != refB || !bytes.Equal(a, b) || len(a) > 65536 {
		t.Fatalf("explain must be byte-identical and under 64 KiB: %s vs %s", refA, refB)
	}
}

func TestConflict_OverlapSameKeyOnly(t *testing.T) {
	existing := rec("a", domain.TypePrice, domain.CustomerKindCustomer, "C1", domain.ProductKindProduct, "P1", "USD", "0", "5")
	existing.ValidTo = time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		mut  func(*domain.Record)
		want bool
	}{
		{"overlapping validity", func(r *domain.Record) { r.ValidFrom = time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC) }, true},
		{"next day does not overlap", func(r *domain.Record) { r.ValidFrom = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC) }, false},
		{"different from quantity is a break", func(r *domain.Record) { r.FromQuantity = d("100") }, false},
		{"different currency", func(r *domain.Record) { r.Currency = "EUR" }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cand := rec("b", domain.TypePrice, domain.CustomerKindCustomer, "C1", domain.ProductKindProduct, "P1", "USD", "0", "6")
			cand.ValidTo = validTo
			c.mut(&cand)
			if _, got := domain.Conflict(cand, []domain.Record{existing}); got != c.want {
				t.Fatalf("conflict = %v; want %v", got, c.want)
			}
		})
	}
}

func TestValidateRecord(t *testing.T) {
	ok := rec("r", domain.TypePrice, domain.CustomerKindAll, "", domain.ProductKindAll, "", "USD", "0", "1")
	if err := domain.ValidateRecord(ok); err != nil {
		t.Fatalf("valid record rejected: %v", err)
	}
	neg := ok
	neg.Amount = d("-1")
	if err := domain.ValidateRecord(neg); err == nil || err.Code != domain.CodeNegative {
		t.Fatalf("negative amount must be %s, got %v", domain.CodeNegative, err)
	}
	bad := ok
	bad.DiscPct1 = d("120")
	bad.CustomerKind = domain.CustomerKindCustomer
	if err := domain.ValidateRecord(bad); err == nil || err.Fields["discPct1"] == "" || err.Fields["customerKey"] == "" {
		t.Fatalf("want field errors for pct and empty customer, got %v", err)
	}
	total := rec("t", domain.TypeTotalDisc, domain.CustomerKindAll, "", domain.ProductKindProduct, "P1", "USD", "0", "1")
	if err := domain.ValidateRecord(total); err == nil || err.Fields["productKind"] == "" {
		t.Fatalf("a product-keyed TOTAL_DISC can never apply and must be refused, got %v", err)
	}
	total.ProductKind, total.ProductKey = domain.ProductKindAll, ""
	if err := domain.ValidateRecord(total); err != nil {
		t.Fatalf("an all-products TOTAL_DISC is valid, got %v", err)
	}
}

func TestSmartRound(t *testing.T) {
	bands := domain.DefaultSmartBands()
	cases := map[string]string{"7.7805": "7.99", "7.99": "7.99", "45.10": "45.95", "150.2": "151", "100": "100"}
	for in, want := range cases {
		if got := domain.SmartRound(d(in), bands); !got.Equal(d(want)) {
			t.Fatalf("SmartRound(%s) = %s; want %s", in, got, want)
		}
	}
}

func TestRebateAccrual(t *testing.T) {
	tiers := []domain.Tier{{From: d("0"), RatePct: d("2")}, {From: d("50000"), RatePct: d("3")}}
	cases := []struct{ name, total, invoice, want string }{
		{"AC-26.8 2% monthly invoice", "10000", "10000", "200"},
		{"tier crossed by cumulative total", "60000", "10000", "300"},
	}
	for _, c := range cases {
		if got := domain.RebateAccrual(tiers, d(c.total), d(c.invoice), decimal.Zero, "USD"); !got.Equal(d(c.want)) {
			t.Fatalf("%s: accrual = %s; want %s", c.name, got, c.want)
		}
	}
	vendor := []domain.Tier{{From: d("0"), RatePct: d("3")}}
	if got := domain.RebateAccrual(vendor, d("20000"), d("20000"), decimal.Zero, "USD"); !got.Equal(d("600")) {
		t.Fatalf("AC-26.11 vendor rebate = %s; want 600", got)
	}
	perUnit := []domain.Tier{{From: d("0"), AmountPerUnit: d("0.25")}}
	if got := domain.RebateAccrual(perUnit, d("40"), d("0"), d("40"), "USD"); !got.Equal(d("10")) {
		t.Fatalf("per-unit accrual = %s; want 10", got)
	}
}

func TestCommissionSplit(t *testing.T) {
	// AC-26.12: 5% of 10,000.00 split 60/40 gives 300.00 and 200.00; the credit note reverses both.
	amount := domain.CommissionAmount(d("10000"), d("5"), "USD")
	shares := []domain.Share{{RepID: "R1", SharePct: d("60")}, {RepID: "R2", SharePct: d("40")}}
	got := domain.SplitCommission(amount, "USD", shares)
	if !got[0].Amount.Equal(d("300")) || !got[1].Amount.Equal(d("200")) {
		t.Fatalf("split = %+v; want 300 and 200", got)
	}
	credit := domain.SplitCommission(amount.Neg(), "USD", shares)
	if !credit[0].Amount.Equal(d("-300")) || !credit[1].Amount.Equal(d("-200")) {
		t.Fatalf("credit split = %+v; want -300 and -200", credit)
	}
	third := domain.SplitCommission(d("10"), "USD", []domain.Share{{RepID: "a", SharePct: d("33.33")}, {RepID: "b", SharePct: d("33.33")}, {RepID: "c", SharePct: d("33.34")}})
	sum := decimal.Zero
	for _, s := range third {
		sum = sum.Add(s.Amount)
	}
	if !sum.Equal(d("10")) {
		t.Fatalf("shares must sum to the amount, got %s", sum)
	}
}

func TestFreeGoods(t *testing.T) {
	// AC-26.13: inclusive 1 per 10, WHOLE_UNITS, 25 ordered -> 23 priced, 2 free.
	inclusive := domain.FreeGoodsRule{Kind: domain.FreeGoodsInclusive, Every: d("10"), Free: d("1"), Calc: "WHOLE_UNITS"}
	if got := domain.FreeGoods(d("25"), inclusive); !got.Priced.Equal(d("23")) || !got.Free.Equal(d("2")) {
		t.Fatalf("inclusive = %+v; want 23 priced, 2 free", got)
	}
	exclusive := inclusive
	exclusive.Kind = domain.FreeGoodsExclusive
	if got := domain.FreeGoods(d("25"), exclusive); !got.Priced.Equal(d("25")) || !got.Free.Equal(d("2")) {
		t.Fatalf("exclusive = %+v; want 25 priced, 2 extra free", got)
	}
	minimum := inclusive
	minimum.MinQty = d("30")
	if got := domain.FreeGoods(d("25"), minimum); !got.Free.IsZero() {
		t.Fatalf("below the minimum no units are free, got %+v", got)
	}
}

func genRecords(n int) []domain.Record {
	out := make([]domain.Record, 0, n)
	kinds := []domain.CustomerKind{domain.CustomerKindCustomer, domain.CustomerKindGroup, domain.CustomerKindAll}
	pkinds := []domain.ProductKind{domain.ProductKindProduct, domain.ProductKindGroup, domain.ProductKindAll}
	for i := 0; i < n; i++ {
		ck, pk := kinds[i%3], pkinds[(i/3)%3]
		r := rec(fmt.Sprintf("R%03d", i), domain.TypePrice, ck, fmt.Sprintf("C%d", i%2+1), pk, "P1", "USD", fmt.Sprint((i%4)*5), fmt.Sprint(10+i%7))
		r.FromQuantity = d(fmt.Sprint([]int{0, 1, 10, 100}[i%4]))
		r.SearchNext = i%5 == 0
		out = append(out, r)
	}
	return out
}

func TestResolve_DeterministicUnderRecordOrder(t *testing.T) {
	// FR-26.4 / AC-26.4: outputs do not depend on the order records are loaded in.
	recs := genRecords(60)
	reversed := make([]domain.Record, len(recs))
	for i, r := range recs {
		reversed[len(recs)-1-i] = r
	}
	lines := []domain.Line{line("1"), line("10"), line("150")}
	ref := domain.Resolve(domain.Input{Header: header("USD"), Lines: lines, Records: recs})
	refJSON, refRef, _ := domain.ExplainTrace(ref)
	for _, order := range [][]domain.Record{reversed, recs} {
		got := domain.Resolve(domain.Input{Header: header("USD"), Lines: lines, Records: order})
		gotJSON, gotRef, _ := domain.ExplainTrace(got)
		if gotRef != refRef || !bytes.Equal(gotJSON, refJSON) {
			t.Fatalf("resolve changed with record order: %s vs %s", gotRef, refRef)
		}
	}
}
