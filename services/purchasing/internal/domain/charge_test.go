package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func chgDay(s string) Day {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return Day{Time: t}
}

func chgDec(s string) *decimal.Decimal { v := decimal.RequireFromString(s); return &v }

func goodCode() ChargeCode {
	return ChargeCode{Code: "FREIGHT", Name: "Freight", Calc: CalcFixed, DefaultValue: decimal.NewFromInt(40), DebitType: ChargeSideItem,
		CreditType: ChargeSideVendor, AllocationRule: AllocateAmount, Status: ChargeActive}
}

func TestChargeCode_Validate(t *testing.T) {
	if err := goodCode().Validate(); err != nil {
		t.Fatalf("good code: %v", err)
	}
	if got := NormalizeChargeCode(ChargeCode{Code: " fr-1 ", Name: " Freight "}); got.Code != "FR-1" || got.Name != "Freight" {
		t.Errorf("normalize = %+v", got)
	}
	for name, mut := range map[string]func(*ChargeCode){
		"empty code":             func(c *ChargeCode) { c.Code = "" },
		"lower case code":        func(c *ChargeCode) { c.Code = "freight" },
		"21 characters":          func(c *ChargeCode) { c.Code = "ABCDEFGHIJKLMNOPQRSTU" },
		"blank name":             func(c *ChargeCode) { c.Name = "" },
		"bad calc":               func(c *ChargeCode) { c.Calc = "WEIGHT" },
		"negative value":         func(c *ChargeCode) { c.DefaultValue = decimal.NewFromInt(-1) },
		"percent above 100":      func(c *ChargeCode) { c.Calc, c.DefaultValue = CalcPercent, decimal.NewFromInt(101) },
		"credit to the item":     func(c *ChargeCode) { c.CreditType = ChargeSideItem },
		"ledger debit, no acct":  func(c *ChargeCode) { c.DebitType = ChargeSideLedger },
		"ledger credit, no acct": func(c *ChargeCode) { c.CreditType = ChargeSideLedger },
		"vendor debit, no vendor": func(c *ChargeCode) {
			c.DebitType, c.CreditType, c.CreditAccountID = ChargeSideVendor, ChargeSideLedger, "acct"
		},
		"vendor debit and credit":    func(c *ChargeCode) { c.DebitType, c.VendorID = ChargeSideVendor, "v" },
		"account on an item debit":   func(c *ChargeCode) { c.DebitAccountID = "acct" },
		"account on a vendor credit": func(c *ChargeCode) { c.CreditAccountID = "acct" },
		"vendor with no vendor side": func(c *ChargeCode) {
			c.CreditType, c.CreditAccountID, c.VendorID = ChargeSideLedger, "acct", "v"
		},
		"bad allocation": func(c *ChargeCode) { c.AllocationRule = "WEIGHT" },
		"bad status":     func(c *ChargeCode) { c.Status = "GONE" },
	} {
		c := goodCode()
		mut(&c)
		if err := c.Validate(); err == nil || err.Code != CodeValidation {
			t.Errorf("%s: err = %v, want COM-0001", name, err)
		}
	}
	c := goodCode()
	c.DebitType, c.DebitAccountID = ChargeSideLedger, "acct"
	if err := c.Validate(); err != nil {
		t.Errorf("ledger debit with an account: %v", err)
	}
	c = goodCode()
	c.VendorID = "forwarder"
	if err := c.Validate(); err != nil {
		t.Errorf("a VENDOR credit payable to another vendor: %v", err)
	}
	c.DebitType, c.CreditType, c.CreditAccountID = ChargeSideVendor, ChargeSideLedger, "acct"
	if err := c.Validate(); err != nil {
		t.Errorf("a VENDOR debit with a LEDGER credit: %v", err)
	}
}

func TestAutoCharge_Validate(t *testing.T) {
	money, pct := goodCode(), goodCode()
	pct.Calc = CalcPercent
	base := AutoCharge{AccountCode: AutoChargeVendor, AccountID: "v", ItemCode: AutoChargeItemAll, Currency: "USD", ValidFrom: chgDay("2026-01-01"), Status: ChargeActive}
	if err := base.Validate(money); err != nil {
		t.Fatalf("good auto charge: %v", err)
	}
	for name, tc := range map[string]struct {
		code ChargeCode
		mut  func(*AutoCharge)
	}{
		"bad key":                  {money, func(a *AutoCharge) { a.AccountCode = "ITEM" }},
		"vendor key, no vendor":    {money, func(a *AutoCharge) { a.AccountID = "" }},
		"ALL key with a vendor":    {money, func(a *AutoCharge) { a.AccountCode = AutoChargeAll }},
		"negative value":           {money, func(a *AutoCharge) { a.Value = chgDec("-1") }},
		"percent value above 100":  {pct, func(a *AutoCharge) { a.Value = chgDec("100.5") }},
		"zero minimum":             {money, func(a *AutoCharge) { a.MinOrderAmount = chgDec("0") }},
		"minimum without currency": {pct, func(a *AutoCharge) { a.MinOrderAmount, a.Currency = chgDec("5"), "" }},
		"money without currency":   {money, func(a *AutoCharge) { a.Currency = "" }},
		"window reversed":          {money, func(a *AutoCharge) { to := chgDay("2025-12-31"); a.ValidTo = &to }},
		"bad item key":             {money, func(a *AutoCharge) { a.ItemCode = "SKU" }},
		"product key, no product":  {money, func(a *AutoCharge) { a.ItemCode = AutoChargeItemProduct }},
		"ALL item with a product":  {money, func(a *AutoCharge) { a.ItemID = "p" }},
		"bad status":               {money, func(a *AutoCharge) { a.Status = "GONE" }},
	} {
		a := base
		tc.mut(&a)
		if err := a.Validate(tc.code); err == nil || err.Code != CodeValidation {
			t.Errorf("%s: err = %v, want COM-0001", name, err)
		}
	}
	a := base
	a.Currency = ""
	if err := a.Validate(pct); err != nil {
		t.Errorf("a PERCENT charge needs no currency: %v", err)
	}
}

func TestAutoCharge_ResolveUsesTheOverrideOrTheDefault(t *testing.T) {
	code := goodCode()
	a := AutoCharge{}
	if calc, v := a.Resolve(code); calc != CalcFixed || !v.Equal(decimal.NewFromInt(40)) {
		t.Errorf("default: %s %s", calc, v)
	}
	a.Value = chgDec("12.5")
	if _, v := a.Resolve(code); !v.Equal(decimal.RequireFromString("12.5")) {
		t.Errorf("override: %s", v)
	}
}

func TestAutoCharge_Applies(t *testing.T) {
	base := AutoCharge{AccountCode: AutoChargeVendor, AccountID: "v1", Currency: "USD", MinOrderAmount: chgDec("500"),
		ValidFrom: chgDay("2026-01-01"), Status: ChargeActive}
	to := chgDay("2026-12-31")
	order := decimal.NewFromInt(500)
	groups := []string{"g1"}
	onDay := chgDay("2026-03-01")
	for _, tc := range []struct {
		name   string
		mut    func(*AutoCharge)
		vendor string
		cur    string
		base   decimal.Decimal
		on     Day
		want   bool
	}{
		{"matches at the minimum", nil, "v1", "USD", order, onDay, true},
		{"other vendor", nil, "v2", "USD", order, onDay, false},
		{"just below the minimum", nil, "v1", "USD", decimal.RequireFromString("499.99"), onDay, false},
		{"other currency", nil, "v1", "EUR", order, onDay, false},
		{"before the window", nil, "v1", "USD", order, chgDay("2025-12-31"), false},
		{"on the last day", func(a *AutoCharge) { a.ValidTo = &to }, "v1", "USD", order, to, true},
		{"after the window", func(a *AutoCharge) { a.ValidTo = &to }, "v1", "USD", order, chgDay("2027-01-01"), false},
		{"inactive", func(a *AutoCharge) { a.Status = ChargeInactive }, "v1", "USD", order, onDay, false},
		{"group member", func(a *AutoCharge) { a.AccountCode, a.AccountID = AutoChargeGroup, "g1" }, "v9", "USD", order, onDay, true},
		{"not in the group", func(a *AutoCharge) { a.AccountCode, a.AccountID = AutoChargeGroup, "g2" }, "v9", "USD", order, onDay, false},
		{"all vendors, any currency", func(a *AutoCharge) {
			a.AccountCode, a.AccountID, a.Currency, a.MinOrderAmount = AutoChargeAll, "", "", nil
		}, "v9", "JPY", decimal.Zero, onDay, true},
	} {
		a := base
		if tc.mut != nil {
			tc.mut(&a)
		}
		if got := a.Applies(tc.vendor, groups, tc.cur, tc.base, tc.on); got != tc.want {
			t.Errorf("%s: Applies = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAutoCharge_MatchesLine(t *testing.T) {
	prod := AutoCharge{ItemCode: AutoChargeItemProduct, ItemID: "p1"}
	grp := AutoCharge{ItemCode: AutoChargeItemGroup, ItemID: "g1"}
	all := AutoCharge{ItemCode: AutoChargeItemAll}
	for name, tc := range map[string]struct {
		a      AutoCharge
		prod   string
		groups []string
		want   bool
	}{
		"product match":     {prod, "p1", nil, true},
		"product mismatch":  {prod, "p2", []string{"g1"}, false},
		"no product":        {prod, "", nil, false},
		"group member":      {grp, "p2", []string{"g0", "g1"}, true},
		"group non-member":  {grp, "p2", []string{"g0"}, false},
		"header is no line": {all, "p1", []string{"g1"}, false},
	} {
		if got := tc.a.MatchesLine(tc.prod, tc.groups); got != tc.want {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
	if all.OnLines() || !prod.OnLines() || !grp.OnLines() {
		t.Error("OnLines: only PRODUCT and GROUP are line charges")
	}
}
