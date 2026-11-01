package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestResolveParameters_SiteThenAllThenDefaults(t *testing.T) {
	rows := []Parameters{{SiteID: AllKey, RAFTolerancePct: decimal.NewFromInt(3), SplitAllocation: AllocProportional}, {SiteID: "S1", RAFTolerancePct: decimal.NewFromInt(7), SplitAllocation: AllocKeepOnOriginal}}
	defaults := Parameters{RAFTolerancePct: decimal.NewFromInt(1)}
	if p, stored := ResolveParameters(rows, "S1", defaults); !stored || !p.RAFTolerancePct.Equal(decimal.NewFromInt(7)) {
		t.Fatalf("site row: %+v %v", p, stored)
	}
	if p, stored := ResolveParameters(rows, "S2", defaults); !stored || !p.RAFTolerancePct.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("All row: %+v %v", p, stored)
	}
	p, stored := ResolveParameters(nil, "S2", defaults)
	if stored || p.SiteID != "S2" || !p.RAFTolerancePct.Equal(decimal.NewFromInt(1)) || p.SplitAllocation != AllocProportional {
		t.Fatalf("defaults: %+v %v", p, stored)
	}
}

func TestParametersValidate(t *testing.T) {
	ok := Parameters{SiteID: AllKey, RAFTolerancePct: decimal.RequireFromString("100"), OverConsumptionPct: decimal.RequireFromString("1000"), SplitAllocation: AllocProportional}
	if f := ok.Validate(); len(f) != 0 {
		t.Fatalf("boundaries are valid: %v", f)
	}
	for name, mut := range map[string]func(*Parameters){
		"siteId":              func(p *Parameters) { p.SiteID = "" },
		"rafTolerancePct":     func(p *Parameters) { p.RAFTolerancePct = decimal.RequireFromString("100.01") },
		"overConsumptionPct":  func(p *Parameters) { p.OverConsumptionPct = decimal.RequireFromString("1000.01") },
		"splitAllocation":     func(p *Parameters) { p.SplitAllocation = "" },
		"pickingListProposal": func(p *Parameters) { p.PickingListProposal = "most" },
		"routeCardProposal":   func(p *Parameters) { p.RouteCardProposal = ProposeNone },
		"staleOrderDays":      func(p *Parameters) { p.StaleOrderDays = 3651 },
	} {
		p := ok
		mut(&p)
		if p.Validate()[name] == "" {
			t.Errorf("%s not refused", name)
		}
	}
	p := ok
	p.RAFTolerancePct = decimal.RequireFromString("1.234")
	if p.Validate()["rafTolerancePct"] == "" {
		t.Error("three decimals must be refused")
	}
	p = ok
	p.RAFTolerancePct = decimal.RequireFromString("2.500")
	if f := p.Validate(); len(f) != 0 {
		t.Errorf("trailing zeros are two decimals: %v", f)
	}
	p = ok
	p.OverConsumptionPct = decimal.NewFromInt(-1)
	if p.Validate()["overConsumptionPct"] == "" {
		t.Error("negative must be refused")
	}
}

func TestCheckAsBuiltRequired(t *testing.T) {
	two := decimal.NewFromInt(2)
	cases := []struct {
		required bool
		qty      decimal.Decimal
		attached int
		refused  bool
	}{{false, two, 0, false}, {true, decimal.Zero, 0, false}, {true, two, 0, true}, {true, two, 1, false}}
	for _, c := range cases {
		err := CheckAsBuiltRequired(c.required, c.qty, c.attached)
		if (err != nil) != c.refused {
			t.Errorf("%+v: err=%v", c, err)
		}
		if err != nil && err.(*Error).Code != CodeAsBuiltRequired {
			t.Errorf("code %v", err)
		}
	}
}

func TestParametersValidate_StaleDaysBoundsAndProposals(t *testing.T) {
	ok := Parameters{SiteID: AllKey, SplitAllocation: AllocProportional, PickingListProposal: ProposeNone, RouteCardProposal: ProposeProportional, StaleOrderDays: 0}
	if f := ok.Validate(); len(f) != 0 {
		t.Fatalf("none, proportional and 0 days are valid: %v", f)
	}
	ok.StaleOrderDays = MaxStaleOrderDays
	if f := ok.Validate(); len(f) != 0 {
		t.Fatalf("3650 days is valid: %v", f)
	}
	ok.StaleOrderDays = -1
	if ok.Validate()["staleOrderDays"] == "" {
		t.Error("negative days must be refused")
	}
}

func TestJournalProposal_SiteDefaults(t *testing.T) {
	p := Parameters{PickingListProposal: ProposeNone, RouteCardProposal: ProposeProportional}
	for typ, want := range map[JournalType]Proposal{JournalPickingList: ProposeNone, JournalRouteCard: ProposeProportional, JournalReportFinished: ProposeProportional} {
		if got := p.JournalProposal(typ); got != want {
			t.Errorf("%s: %s, want %s", typ, got, want)
		}
	}
	if got := (Parameters{}).JournalProposal(JournalPickingList); got != ProposeRemaining {
		t.Errorf("unset picking list: %s", got)
	}
	if got := (Parameters{}).JournalProposal(JournalRouteCard); got != ProposeRemaining {
		t.Errorf("unset route card: %s", got)
	}
	if p, _ := ResolveParameters(nil, "S", Parameters{}); p.PickingListProposal != ProposeRemaining || p.RouteCardProposal != ProposeRemaining {
		t.Errorf("defaults: %+v", p)
	}
}
