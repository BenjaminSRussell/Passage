package domain

import (
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
		{"markup at 1000 percent", func(p *Partner) { p.MarkupPct = decimal.NewFromInt(1000) }, true},
		{"markup over 1000 percent", func(p *Partner) { p.MarkupPct = decimal.RequireFromString("1000.01") }, false},
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
