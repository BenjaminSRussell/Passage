package domain_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func op(no, next int, priority string, setup, run string) domain.RouteOperation {
	return domain.RouteOperation{No: no, NextNo: next, Priority: priority, SetupH: dec(setup), RunH: dec(run), ProcessQty: dec("1")}
}

type leadTimeFixture struct {
	Name          string
	Quantity      string
	HoursPerDay   string
	Operations    []domain.RouteOperation
	ExpectedHours string
	ExpectedDays  int
}

func loadFixtures(t *testing.T) []leadTimeFixture {
	t.Helper()
	var fixtures []leadTimeFixture
	raw, err := os.ReadFile("../../testdata/routing-leadtime.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures
}

func TestLeadTimeFixtures(t *testing.T) {
	fixtures := loadFixtures(t)
	if len(fixtures) != 12 {
		t.Fatalf("fixtures = %d, want 12", len(fixtures))
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			got, err := domain.LeadTime(f.Operations, dec(f.Quantity), dec(f.HoursPerDay))
			if err != nil {
				t.Fatal(err)
			}
			if !got.Hours.Equal(dec(f.ExpectedHours)) || got.CalendarDays != f.ExpectedDays {
				t.Fatalf("got %s h / %d d, want %s h / %d d", got.Hours, got.CalendarDays, f.ExpectedHours, f.ExpectedDays)
			}
		})
	}
}

func TestLeadTimeErrors(t *testing.T) {
	zeroProcessQty := op(10, 0, "", "1", "1")
	zeroProcessQty.ProcessQty = decimal.Zero
	negative := op(10, 0, "", "-1", "1")
	tests := []struct {
		name string
		ops  []domain.RouteOperation
		qty  string
		want string
	}{
		{"missing next operation", []domain.RouteOperation{op(10, 99, "", "1", "1")}, "100", domain.CodeRouteNextMiss},
		{"secondary without primary at its join", []domain.RouteOperation{op(10, 40, "", "1", "1"), op(20, 30, "secondary1", "1", "1"), op(30, 40, "", "1", "1"), op(40, 0, "", "1", "1")}, "100", domain.CodeRouteNoPrimary},
		{"cycle", []domain.RouteOperation{op(10, 20, "", "1", "1"), op(20, 10, "", "1", "1")}, "100", domain.CodeRouteCycle},
		{"zero process quantity", []domain.RouteOperation{zeroProcessQty}, "100", domain.CodeRouteQty},
		{"negative time", []domain.RouteOperation{negative}, "100", domain.CodeRouteQty},
		{"negative quantity", []domain.RouteOperation{op(10, 0, "", "1", "1")}, "-5", domain.CodeRouteQty},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := domain.LeadTime(tt.ops, dec(tt.qty), dec("8")); !domain.HasCode(err, tt.want) {
				t.Fatalf("err = %v, want %s", err, tt.want)
			}
		})
	}
}

func TestLeadTimeNeverShrinksWithQuantity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 5).Draw(t, "operations")
		ops := make([]domain.RouteOperation, n)
		for i := range ops {
			ops[i] = domain.RouteOperation{
				No: (i + 1) * 10, SetupH: decimal.NewFromInt(int64(rapid.IntRange(0, 5).Draw(t, "setup"))),
				RunH: decimal.NewFromInt(int64(rapid.IntRange(0, 5).Draw(t, "run"))), ProcessQty: decimal.NewFromInt(int64(rapid.IntRange(1, 20).Draw(t, "pq"))),
			}
		}
		small := int64(rapid.IntRange(1, 500).Draw(t, "small"))
		large := small + int64(rapid.IntRange(0, 500).Draw(t, "extra"))
		a, errA := domain.LeadTime(ops, decimal.NewFromInt(small), dec("8"))
		b, errB := domain.LeadTime(ops, decimal.NewFromInt(large), dec("8"))
		if errA != nil || errB != nil {
			t.Fatal(errA, errB)
		}
		if b.Hours.LessThan(a.Hours) {
			t.Fatalf("qty %d gives %s h but qty %d gives %s h", small, a.Hours, large, b.Hours)
		}
	})
}

var routeStart = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

func routeVersion(ops ...domain.VersionOperation) domain.RouteVersion {
	return domain.RouteVersion{ID: "rv", ProductID: "P1", SiteID: "S1", RouteCode: "R", VersionNo: 1, EffectiveFrom: routeStart, Operations: ops}
}

func versionOp(no, next int, reqs ...domain.OperationRequirement) domain.VersionOperation {
	return domain.VersionOperation{RouteOperation: op(no, next, "", "1", "1"), LinkType: domain.LinkHard, Requirements: reqs}
}

func secondaryOp(no, next int) domain.VersionOperation {
	o := versionOp(no, next)
	o.Priority = "secondary1"
	return o
}

func TestValidateRouteVersionAcceptsSecondaryBesidePrimary(t *testing.T) {
	if err := domain.ValidateRouteVersion(routeVersion(versionOp(10, 30), secondaryOp(20, 30), versionOp(30, 0))); err != nil {
		t.Fatalf("parallel branch joining at 30: %v", err)
	}
}

func TestValidateRouteVersionAcceptsLeadTimeFixtures(t *testing.T) {
	for _, f := range loadFixtures(t) {
		v := routeVersion()
		for _, o := range f.Operations {
			v.Operations = append(v.Operations, domain.VersionOperation{RouteOperation: o})
		}
		if err := domain.ValidateRouteVersion(v); err != nil {
			t.Errorf("%s: %v", f.Name, err)
		}
	}
}

func TestValidateRouteVersionRejects(t *testing.T) {
	many := make([]domain.VersionOperation, 501)
	for i := range many {
		many[i] = versionOp(i+1, 0)
	}
	mutated := func(change func(*domain.VersionOperation)) domain.RouteVersion {
		o := versionOp(10, 0)
		change(&o)
		return routeVersion(o)
	}
	tests := []struct {
		name string
		v    domain.RouteVersion
		want string
	}{
		{"over 500 operations", routeVersion(many...), domain.CodeRouteOpsLimit},
		{"duplicate number", routeVersion(versionOp(10, 0), versionOp(10, 0)), domain.CodeRouteOpDup},
		{"dangling next", routeVersion(versionOp(10, 99)), domain.CodeRouteNext},
		{"self loop", routeVersion(versionOp(10, 10)), domain.CodeRouteCycle},
		{"cycle", routeVersion(versionOp(10, 20), versionOp(20, 30), versionOp(30, 10)), domain.CodeRouteCycle},
		{"no operations", routeVersion(), domain.CodeRouteQty},
		{"five decimal hours", mutated(func(o *domain.VersionOperation) { o.RunH = dec("1.00001") }), domain.CodeRouteQty},
		{"bad priority", mutated(func(o *domain.VersionOperation) { o.Priority = "secondary9" }), domain.CodeRouteQty},
		{"secondary without primary at its join", routeVersion(versionOp(10, 40), secondaryOp(20, 30), versionOp(30, 40), versionOp(40, 0)), domain.CodeRouteNoPrimary},
		{"bad link type", mutated(func(o *domain.VersionOperation) { o.LinkType = "firm" }), domain.CodeRouteQty},
		{"operation id too long", mutated(func(o *domain.VersionOperation) { o.OperationID = "123456789012345678901" }), domain.CodeRouteQty},
		{"skill requirement", routeVersion(versionOp(10, 0, domain.OperationRequirement{Kind: "skill", RefID: "x", Quantity: 1})), domain.CodeRouteReqKind},
		{"zero quantity", routeVersion(versionOp(10, 0, domain.OperationRequirement{Kind: domain.ReqType, RefID: "machine"})), domain.CodeRouteReqKind},
		{"missing product", domain.RouteVersion{Operations: []domain.VersionOperation{versionOp(10, 0)}}, domain.CodeRouteVersion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := domain.ValidateRouteVersion(tt.v); !domain.HasCode(err, tt.want) {
				t.Fatalf("err = %v, want %s", err, tt.want)
			}
		})
	}
}

func TestValidateRouteVersionCycleIffNextChainLoops(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 8).Draw(t, "operations")
		v := routeVersion()
		next := make([]int, n)
		for i := range next {
			next[i] = rapid.IntRange(0, n).Draw(t, "next")
			v.Operations = append(v.Operations, versionOp(i+1, next[i]))
		}
		looped := false
		for start := 1; start <= n; start++ {
			at := start
			for step := 0; at != 0 && step <= n; step++ {
				at = next[at-1]
			}
			looped = looped || at != 0
		}
		if got := domain.HasCode(domain.ValidateRouteVersion(v), domain.CodeRouteCycle); got != looped {
			t.Fatalf("next %v: cycle reported = %v, want %v", next, got, looped)
		}
	})
}

func TestCheckRequirements(t *testing.T) {
	effective := func(from, to time.Time) []domain.ResourceCapability {
		return []domain.ResourceCapability{{CapabilityID: "weld", Level: 3, EffectiveFrom: from, EffectiveTo: to}}
	}
	machine := domain.Resource{ID: "m1", Type: domain.TypeMachine, SiteID: "S1", GroupID: "g1", Capabilities: effective(routeStart.AddDate(0, -1, 0), time.Time{})}
	second := domain.Resource{ID: "m2", Type: domain.TypeMachine, SiteID: "S1", GroupID: "g1"}
	elsewhere := domain.Resource{ID: "m3", Type: domain.TypeMachine, SiteID: "S2", GroupID: "g1", Capabilities: effective(routeStart.AddDate(0, -1, 0), time.Time{})}
	expired := domain.Resource{ID: "m4", Type: domain.TypeMachine, SiteID: "S1", Capabilities: effective(routeStart.AddDate(-1, 0, 0), routeStart.AddDate(0, 0, -1))}
	future := domain.Resource{ID: "m5", Type: domain.TypeMachine, SiteID: "S1", Capabilities: effective(routeStart.AddDate(0, 0, 1), time.Time{})}
	req := func(kind, ref string, level, qty int) domain.OperationRequirement {
		return domain.OperationRequirement{Kind: kind, RefID: ref, Level: level, Quantity: qty}
	}
	tests := []struct {
		name      string
		resources []domain.Resource
		req       domain.OperationRequirement
		ok        bool
	}{
		{"resource present", []domain.Resource{machine}, req(domain.ReqResource, "m1", 0, 1), true},
		{"resource at another site", []domain.Resource{elsewhere}, req(domain.ReqResource, "m3", 0, 1), false},
		{"group big enough", []domain.Resource{machine, second}, req(domain.ReqGroup, "g1", 0, 2), true},
		{"group too small", []domain.Resource{machine, elsewhere}, req(domain.ReqGroup, "g1", 0, 2), false},
		{"capability at level", []domain.Resource{machine}, req(domain.ReqCapability, "weld", 3, 1), true},
		{"capability level too low", []domain.Resource{machine}, req(domain.ReqCapability, "weld", 4, 1), false},
		{"capability covering effective date", []domain.Resource{machine}, req(domain.ReqCapability, "weld", 1, 1), true},
		{"capability ended before effective date", []domain.Resource{expired}, req(domain.ReqCapability, "weld", 1, 1), false},
		{"capability starting after effective date", []domain.Resource{future}, req(domain.ReqCapability, "weld", 1, 1), false},
		{"capability quantity short", []domain.Resource{machine, second}, req(domain.ReqCapability, "weld", 1, 2), false},
		{"type enough", []domain.Resource{machine, second}, req(domain.ReqType, domain.TypeMachine, 0, 2), true},
		{"type short", []domain.Resource{machine, elsewhere}, req(domain.ReqType, domain.TypeMachine, 0, 2), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := domain.CheckRequirements(routeVersion(versionOp(10, 0), versionOp(20, 0, tt.req)), tt.resources)
			if tt.ok != (err == nil) || (!tt.ok && (!domain.HasCode(err, domain.CodeRouteUnsatisfied) || !strings.Contains(err.Error(), "operation 20 requirement[0]"))) {
				t.Fatalf("err = %v, want satisfied = %v", err, tt.ok)
			}
		})
	}
}

func TestCheckRouteNoOverlap(t *testing.T) {
	active := func(id, site string, from, to time.Time) domain.RouteVersion {
		return domain.RouteVersion{ID: id, ProductID: "P1", SiteID: site, Status: domain.StatusActive, EffectiveFrom: from, EffectiveTo: to}
	}
	candidate := domain.RouteVersion{ID: "new", ProductID: "P1", SiteID: "S1", EffectiveFrom: routeStart}
	tests := []struct {
		name     string
		existing domain.RouteVersion
		overlap  bool
	}{
		{"open ended active", active("a", "S1", routeStart.AddDate(-1, 0, 0), time.Time{}), true},
		{"ended before", active("a", "S1", routeStart.AddDate(-1, 0, 0), routeStart.AddDate(0, 0, -1)), false},
		{"other site", active("a", "S2", routeStart.AddDate(-1, 0, 0), time.Time{}), false},
		{"draft ignored", domain.RouteVersion{ID: "a", ProductID: "P1", SiteID: "S1", Status: domain.StatusDraft, EffectiveFrom: routeStart}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := domain.CheckRouteNoOverlap([]domain.RouteVersion{tt.existing}, candidate); domain.HasCode(err, domain.CodeRouteOverlap) != tt.overlap {
				t.Fatalf("err = %v, want overlap = %v", err, tt.overlap)
			}
		})
	}
}
