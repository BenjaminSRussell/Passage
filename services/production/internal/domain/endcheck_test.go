package domain

import (
	"slices"
	"testing"

	"github.com/shopspring/decimal"
)

func codes(fs []EndFinding) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.Code)
	}
	return out
}

func TestEvaluateEnd(t *testing.T) {
	d := decimal.RequireFromString
	ready := EndFacts{Status: ReportedAsFinished, QtyRAF: d("100"), Today: "2026-11-08", LastPostedOn: "2026-11-08"}
	tests := []struct {
		name     string
		mutate   func(*EndFacts)
		blockers []string
		warnings []string
	}{
		{"ready", func(*EndFacts) {}, nil, nil},
		{"created cannot end", func(f *EndFacts) { f.Status = Created }, []string{CodeIllegalTransition}, nil},
		{"ended cannot end again", func(f *EndFacts) { f.Status = Ended }, []string{CodeIllegalTransition}, nil},
		{"started with output must be reported", func(f *EndFacts) { f.Status = Started }, []string{CodeNotRAFed}, nil},
		{"started without output needs the duty", func(f *EndFacts) { f.Status = Started; f.QtyRAF = decimal.Zero }, []string{CodeZeroOutput}, nil},
		{"started without output with the duty", func(f *EndFacts) { f.Status = Started; f.QtyRAF = decimal.Zero; f.ZeroOutputDuty = true }, nil, nil},
		{"unposted journals", func(f *EndFacts) { f.UnpostedJournals = 2 }, []string{CodeUnpostedJournals}, nil},
		{"posting date in the future", func(f *EndFacts) { f.LastPostedOn = "2026-11-09" }, []string{CodeEndBeforeLastPosted}, nil},
		{"as-built required and missing", func(f *EndFacts) { f.AsBuiltRequired = true }, []string{CodeAsBuiltRequired}, nil},
		{"as-built required and attached", func(f *EndFacts) { f.AsBuiltRequired = true; f.AttachedAsBuilt = 1 }, nil, nil},
		{"posting date today is fine", func(f *EndFacts) { f.LastPostedOn = f.Today }, nil, nil},
		{"no posting profile", func(f *EndFacts) {
			f.Settlement = &EndFinding{Code: CodeNoPostingProfile, Message: "no posting profile resolves for item group All at site s"}
		}, []string{CodeNoPostingProfile}, nil},
		{"ended order reports the status alone", func(f *EndFacts) { f.Status = Ended; f.UnpostedJournals = 3; f.AsBuiltRequired = true }, []string{CodeIllegalTransition}, nil},
		{"fully assigned serials do not warn", func(f *EndFacts) {
			f.Components = []ComponentCoverage{{Consumed: d("2"), Assigned: d("2")}}
		}, nil, nil},
		{"unassigned serials warn", func(f *EndFacts) {
			f.Components = []ComponentCoverage{{Consumed: d("2"), Assigned: d("1")}}
		}, nil, []string{CodeAsBuiltUnresolved}},
		{"every blocker in the order :end applies them, and a warning", func(f *EndFacts) {
			f.UnpostedJournals = 1
			f.AsBuiltRequired = true
			f.LastPostedOn = "2026-11-09"
			f.Settlement = &EndFinding{Code: CodeNoPostingProfile, Message: "no profile"}
			f.Components = []ComponentCoverage{{Consumed: d("1"), Assigned: decimal.Zero}}
		}, []string{CodeAsBuiltRequired, CodeUnpostedJournals, CodeEndBeforeLastPosted, CodeNoPostingProfile}, []string{CodeAsBuiltUnresolved}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := ready
			tc.mutate(&f)
			got := EvaluateEnd(f)
			if got.Blockers == nil || got.Warnings == nil {
				t.Fatal("blockers and warnings are lists, never null")
			}
			if g := codes(got.Blockers); !slices.Equal(g, tc.blockers) {
				t.Fatalf("blockers %v, want %v", g, tc.blockers)
			}
			if g := codes(got.Warnings); !slices.Equal(g, tc.warnings) {
				t.Fatalf("warnings %v, want %v", g, tc.warnings)
			}
			if got.CanEnd != (len(tc.blockers) == 0) {
				t.Fatalf("canEnd %v with blockers %v", got.CanEnd, tc.blockers)
			}
		})
	}
}

func TestUnresolvedSerials(t *testing.T) {
	d := decimal.RequireFromString
	got := UnresolvedSerials([]ComponentCoverage{
		{Consumed: d("3"), Assigned: d("1")},   // 2 open
		{Consumed: d("2.5"), Assigned: d("2")}, // 0.5 open rounds up to 1
		{Consumed: d("1"), Assigned: d("1")},   // none
		{Consumed: d("1"), Assigned: d("2")},   // over-assigned never goes negative
	})
	if got != 3 {
		t.Fatalf("unresolved serials %d, want 3", got)
	}
}

// The unposted-journal and end-date guards :end applies carry the PRD message and count.
func TestEndJournalGuards(t *testing.T) {
	got := EvaluateEnd(EndFacts{Status: ReportedAsFinished, QtyRAF: decimal.NewFromInt(1), UnpostedJournals: 2, LastPostedOn: "2026-11-09", Today: "2026-11-08"})
	if len(got.Blockers) != 2 || got.Blockers[0].Count != 2 || got.Blockers[0].Message != "cannot end: 2 journals are unposted; post or delete them" ||
		got.Blockers[1].Message != "the end date 2026-11-08 is before the last journal posting date 2026-11-09" {
		t.Fatalf("blockers %+v", got.Blockers)
	}
	if CheckUnpostedJournals(0) != nil || CheckEndDate("", "2026-11-08") != nil || CheckEndDate("2026-11-08", "2026-11-08") != nil {
		t.Fatal("no draft, no posting or a posting today does not refuse End")
	}
}
