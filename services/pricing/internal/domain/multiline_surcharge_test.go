package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func msDay(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func msPriceFor(id, product, amount string) Record {
	return Record{ID: id, Status: StatusActive, Type: TypePrice, CustomerKind: CustomerKindAll,
		ProductKind: ProductKindProduct, ProductKey: product, Currency: "USD",
		Amount: decimal.RequireFromString(amount), ValidFrom: msDay("2026-01-01"), ValidTo: msDay("2026-12-31")}
}

func msLine(no int, product string, qty string) Line {
	return Line{No: no, ProductID: product, ProductGroupID: "G", Unit: "EA", Quantity: decimal.RequireFromString(qty)}
}

func msHeader() Header {
	return Header{CustomerID: "C1", Currency: "USD", PricingDate: msDay("2026-06-15")}
}

func TestResolveMultilineDiscount(t *testing.T) {
	multiline := Record{ID: "M1", Status: StatusActive, Type: TypeMultiline, CustomerKind: CustomerKindAll,
		ProductKind: ProductKindGroup, ProductKey: "G", Currency: "USD", FromQuantity: decimal.NewFromInt(10),
		Amount: decimal.NewFromInt(1), ValidFrom: msDay("2026-01-01"), ValidTo: msDay("2026-12-31")}
	prices := []Record{msPriceFor("P1", "P1", "10"), msPriceFor("P2", "P2", "10")}
	cases := []struct {
		name      string
		lines     []Line
		wantUnit  []string
		wantMulti bool
	}{
		{"group sum reaches threshold across lines", []Line{msLine(1, "P1", "5"), msLine(2, "P2", "5")}, []string{"9", "9"}, true},
		{"group sum below threshold", []Line{msLine(1, "P1", "5")}, []string{"10"}, false},
		{"threshold is inclusive", []Line{msLine(1, "P1", "4"), msLine(2, "P2", "6")}, []string{"9", "9"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Resolve(Input{Header: msHeader(), Lines: tc.lines, Records: append(append([]Record{}, prices...), multiline)})
			for i, lr := range res.Lines {
				if lr.Error != "" {
					t.Fatalf("line %d unpriced: %s", i+1, lr.ErrorDetail)
				}
				if !lr.NetUnit.Equal(decimal.RequireFromString(tc.wantUnit[i])) {
					t.Fatalf("line %d net unit = %s, want %s", i+1, lr.NetUnit, tc.wantUnit[i])
				}
				has := false
				for _, a := range lr.Applied {
					if a.Type == TypeMultiline {
						has = true
					}
				}
				if has != tc.wantMulti {
					t.Fatalf("line %d multiline applied = %v, want %v", i+1, has, tc.wantMulti)
				}
			}
		})
	}
}

func TestResolveMultilineIgnoresLinesOutsideGroup(t *testing.T) {
	multiline := Record{ID: "M1", Status: StatusActive, Type: TypeMultiline, CustomerKind: CustomerKindAll,
		ProductKind: ProductKindAll, Currency: "USD", FromQuantity: decimal.NewFromInt(10),
		Amount: decimal.NewFromInt(1), ValidFrom: msDay("2026-01-01"), ValidTo: msDay("2026-12-31")}
	price := msPriceFor("P1", "P1", "10")
	line := Line{No: 1, ProductID: "P1", Unit: "EA", Quantity: decimal.NewFromInt(5)}
	res := Resolve(Input{Header: msHeader(), Lines: []Line{line}, Records: []Record{price, multiline}})
	if got := res.Lines[0].NetUnit; !got.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("ungrouped line must price on its own quantity: net %s, want 10", got)
	}
}

func TestResolveSmallOrderSurcharge(t *testing.T) {
	price := msPriceFor("P1", "P1", "10")
	fixed := Record{ID: "S1", Status: StatusActive, Type: TypeSurcharge, CustomerKind: CustomerKindAll,
		ProductKind: ProductKindAll, Currency: "USD", Amount: decimal.NewFromInt(5),
		ValidFrom: msDay("2026-01-01"), ValidTo: msDay("2026-12-31")}
	pct := fixed
	pct.ID, pct.Amount, pct.DiscPct1 = "S2", decimal.Zero, decimal.NewFromInt(10)
	cases := []struct {
		name      string
		qty       string
		threshold decimal.Decimal
		surcharge Record
		wantNet   string
		wantApply bool
		wantSur   string
	}{
		{"fixed surcharge below threshold", "10", decimal.NewFromInt(250), fixed, "105", true, "5"},
		{"percent surcharge below threshold", "10", decimal.NewFromInt(250), pct, "110", true, "10"},
		{"at or above threshold no surcharge", "25", decimal.NewFromInt(250), fixed, "250", false, "0"},
		{"no threshold no surcharge", "10", decimal.Zero, fixed, "100", false, "0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := Line{No: 1, ProductID: "P1", Unit: "EA", Quantity: decimal.RequireFromString(tc.qty)}
			res := Resolve(Input{Header: msHeader(), Lines: []Line{line}, Records: []Record{price, tc.surcharge}, SmallOrderThreshold: tc.threshold})
			if !res.OrderNet.Equal(decimal.RequireFromString(tc.wantNet)) {
				t.Fatalf("order net = %s, want %s", res.OrderNet, tc.wantNet)
			}
			applied := false
			for _, a := range res.Applied {
				if a.Type == TypeSurcharge {
					applied = true
				}
			}
			if applied != tc.wantApply {
				t.Fatalf("surcharge applied = %v, want %v", applied, tc.wantApply)
			}
			if !res.Surcharge.Equal(decimal.RequireFromString(tc.wantSur)) || !res.Subtotal.Add(res.Surcharge).Sub(res.TotalDiscount).Equal(res.OrderNet) {
				t.Fatalf("surcharge = %s, subtotal %s, order net %s; want surcharge %s and net = subtotal - total discount + surcharge",
					res.Surcharge, res.Subtotal, res.OrderNet, tc.wantSur)
			}
		})
	}
}

func TestSmallOrderThresholdByCurrency(t *testing.T) {
	cases := map[string]string{"USD": "250", "EUR": "200", "JPY": "0"}
	for cur, want := range cases {
		if got := SmallOrderThreshold(cur); !got.Equal(decimal.RequireFromString(want)) {
			t.Fatalf("threshold(%s) = %s, want %s", cur, got, want)
		}
	}
}

func TestValidateRecordAcceptsNewTypes(t *testing.T) {
	base := Record{ID: "x", Currency: "USD", CustomerKind: CustomerKindAll, ProductKind: ProductKindAll,
		ValidFrom: msDay("2026-01-01"), ValidTo: msDay("2026-12-31")}
	for _, typ := range []ConditionType{TypeMultiline, TypeSurcharge, TypePrice} {
		r := base
		r.Type = typ
		if err := ValidateRecord(r); err != nil {
			t.Fatalf("%s rejected: %v", typ, err)
		}
	}
	r := base
	r.Type = "FREIGHT"
	if err := ValidateRecord(r); err == nil {
		t.Fatal("unknown condition type must be rejected")
	}
}

// A surcharge record carries a unit only by accident: the order has no single unit, so the
// unit is ignored. An order whose every line is unpriced gets no surcharge.
func TestResolveSurchargeIgnoresUnitAndSkipsUnpricedOrders(t *testing.T) {
	price := msPriceFor("P1", "P1", "10")
	sur := Record{ID: "S1", Status: StatusActive, Type: TypeSurcharge, CustomerKind: CustomerKindAll, ProductKind: ProductKindAll,
		Currency: "USD", Unit: "BOX", Amount: decimal.NewFromInt(5), ValidFrom: msDay("2026-01-01"), ValidTo: msDay("2026-12-31")}
	line := Line{No: 1, ProductID: "P1", Unit: "EA", Quantity: decimal.NewFromInt(2)}
	res := Resolve(Input{Header: msHeader(), Lines: []Line{line}, Records: []Record{price, sur}, SmallOrderThreshold: decimal.NewFromInt(250)})
	if !res.Surcharge.Equal(decimal.NewFromInt(5)) || !res.OrderNet.Equal(decimal.NewFromInt(25)) {
		t.Fatalf("surcharge %s order net %s; want 5 and 25", res.Surcharge, res.OrderNet)
	}
	unpriced := Line{No: 1, ProductID: "NOPRICE", Unit: "EA", Quantity: decimal.NewFromInt(2)}
	res = Resolve(Input{Header: msHeader(), Lines: []Line{unpriced}, Records: []Record{price, sur}, SmallOrderThreshold: decimal.NewFromInt(250)})
	if !res.Surcharge.IsZero() || !res.OrderNet.IsZero() {
		t.Fatalf("an order with no priced line must not be surcharged: surcharge %s net %s", res.Surcharge, res.OrderNet)
	}
}

func TestValidateRecordKeysOfNewTypes(t *testing.T) {
	base := Record{ID: "x", Currency: "USD", CustomerKind: CustomerKindAll, ValidFrom: msDay("2026-01-01"), ValidTo: msDay("2026-12-31")}
	cases := []struct {
		name  string
		typ   ConditionType
		kind  ProductKind
		key   string
		pct2  string
		field string
	}{
		{"multiline by product group", TypeMultiline, ProductKindGroup, "G", "0", ""},
		{"multiline by product", TypeMultiline, ProductKindProduct, "P1", "0", "productKind"},
		{"surcharge for one product", TypeSurcharge, ProductKindProduct, "P1", "0", "productKind"},
		{"surcharge with a second percentage", TypeSurcharge, ProductKindAll, "", "5", "discPct2"},
	}
	for _, tc := range cases {
		r := base
		r.Type, r.ProductKind, r.ProductKey, r.DiscPct2 = tc.typ, tc.kind, tc.key, decimal.RequireFromString(tc.pct2)
		err := ValidateRecord(r)
		if tc.field == "" {
			if err != nil {
				t.Fatalf("%s: rejected %v", tc.name, err.Fields)
			}
			continue
		}
		if err == nil || err.Fields[tc.field] == "" {
			t.Fatalf("%s: want field %s rejected, got %v", tc.name, tc.field, err)
		}
	}
}
