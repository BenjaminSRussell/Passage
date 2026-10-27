package domain

import "testing"

func TestCoverageGroupValidate(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*CoverageGroupSpec)
		field  string // empty means valid
	}{
		{name: "defaults are valid", mutate: func(*CoverageGroupSpec) {}},
		{name: "bad code", mutate: func(g *CoverageGroupSpec) { g.Code = "x" }, field: "code"},
		{name: "unknown coverage code", mutate: func(g *CoverageGroupSpec) { g.CoverageCode = "weekly" }, field: "coverageCode"},
		{name: "zero coverage fence", mutate: func(g *CoverageGroupSpec) { g.CoverageTimeFenceDays = 0 }, field: "coverageTimeFenceDays"},
		{name: "negative days out of range", mutate: func(g *CoverageGroupSpec) { g.NegativeDays = 366 }, field: "negativeDays"},
		{name: "period without period days", mutate: func(g *CoverageGroupSpec) { g.CoverageCode = CoveragePeriod }, field: "periodDays"},
		{name: "period with period days", mutate: func(g *CoverageGroupSpec) { g.CoverageCode = CoveragePeriod; g.PeriodDays = 7 }},
		{name: "fence beyond max", mutate: func(g *CoverageGroupSpec) { g.FreezeFenceDays = 3651 }, field: "freezeFenceDays"},
		{name: "auto-firm without firming fence", mutate: func(g *CoverageGroupSpec) { g.AutoFirm = true }, field: "autoFirm"},
		{name: "auto-firm with firming fence", mutate: func(g *CoverageGroupSpec) { g.AutoFirm = true; g.FirmingFenceDays = 5 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := DefaultCoverageGroup("REQ", CoverageRequirement)
			tc.mutate(&g)
			errs := g.Validate()
			if tc.field == "" {
				if errs != nil {
					t.Fatalf("unexpected errors: %v", errs)
				}
				return
			}
			if _, ok := errs[tc.field]; !ok {
				t.Fatalf("want error on %s, got %v", tc.field, errs)
			}
		})
	}
}

func TestDefaultCoverageGroup_FR22_2(t *testing.T) {
	g := DefaultCoverageGroup("REQ", CoverageRequirement)
	if g.CoverageTimeFenceDays != 100 || g.NegativeDays != 1 || g.PositiveDays != 0 {
		t.Fatalf("defaults = %+v, want fence 100, negative 1, positive 0", g)
	}
	if !g.GenerateActions || !g.GenerateFutures || g.AutoFirm {
		t.Fatalf("message defaults wrong: %+v", g)
	}
}
