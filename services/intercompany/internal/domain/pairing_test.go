package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func TestValidatePartner_Table(t *testing.T) {
	base := activePartner()
	cases := []struct {
		name string
		edit func(*Partner)
		ok   bool
	}{
		{"valid", func(*Partner) {}, true},
		{"same entity", func(p *Partner) { p.BuyerEntity = p.SellerEntity }, false},
		{"unknown pricing", func(p *Partner) { p.Pricing = "Guess" }, false},
		{"negative markup", func(p *Partner) { p.MarkupPct = decimal.NewFromInt(-1) }, false},
		{"price tolerance over 100", func(p *Partner) { p.Tolerance.PricePct = decimal.NewFromInt(101) }, false},
		{"date tolerance over 30", func(p *Partner) { p.Tolerance.DateDays = 31 }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := base
			c.edit(&p)
			if err := ValidatePartner(p); (err == nil) != c.ok {
				t.Fatalf("err = %v; want ok=%v", err, c.ok)
			}
		})
	}
}

func TestActivePartner_OneActivePerDirection(t *testing.T) {
	inactive := activePartner()
	inactive.Active = false
	dup := activePartner()
	if _, err := ActivePartner([]Partner{inactive}, "A", "B"); err == nil {
		t.Fatal("inactive pairing resolved")
	}
	if _, err := ActivePartner([]Partner{activePartner(), dup}, "A", "B"); err == nil {
		t.Fatal("two active pairings resolved")
	}
	reverse := activePartner()
	reverse.SellerEntity, reverse.BuyerEntity = "B", "A"
	if p, err := ActivePartner([]Partner{reverse, activePartner()}, "B", "A"); err != nil || p.SellerEntity != "B" {
		t.Fatalf("reverse direction = %+v, %v", p, err)
	}
}

func TestPriceLine_Policies(t *testing.T) {
	cost := activePartner()
	cost.Pricing, cost.MarkupPct = PriceCostPlus, decimal.NewFromInt(10)
	got, err := PriceLine(cost, decimal.Zero, decimal.NewFromInt(60))
	if err != nil || !got.Equal(decimal.NewFromInt(66)) {
		t.Fatalf("cost plus = %s, %v; want 66", got, err)
	}
	list, err := PriceLine(activePartner(), decimal.NewFromInt(25), decimal.Zero)
	if err != nil || !list.Equal(decimal.NewFromInt(25)) {
		t.Fatalf("price list = %s, %v", list, err)
	}
	table := activePartner()
	table.Pricing = PriceTable
	var de *Error
	if _, err := PriceLine(table, decimal.Zero, decimal.Zero); !errors.As(err, &de) || de.Code != CodeTransferPrice {
		t.Fatalf("table err = %v; want IC-0821", err)
	}
}

func TestWithinTolerance_Table(t *testing.T) {
	tol := DefaultTolerance()
	q := decimal.NewFromInt(100)
	p := decimal.NewFromInt(25)
	cases := []struct {
		name     string
		newQty   decimal.Decimal
		newPrice decimal.Decimal
		shift    int
		want     bool
	}{
		{"unchanged", q, p, 0, true},
		{"quantity change needs re-approval", decimal.NewFromInt(101), p, 0, false},
		{"price up 0.4 percent is silent", q, decimal.RequireFromString("25.10"), 0, true},
		{"price up 1 percent needs re-approval", q, decimal.RequireFromString("25.25"), 0, false},
		{"date two days later is silent", q, p, 2, true},
		{"date three days later needs re-approval", q, p, 3, false},
		{"date two days earlier is silent", q, p, -2, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := WithinTolerance(tol, q, c.newQty, p, c.newPrice, c.shift); got != c.want {
				t.Fatalf("got %v; want %v", got, c.want)
			}
		})
	}
}
