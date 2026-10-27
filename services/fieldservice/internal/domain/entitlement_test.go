package domain

import (
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func d(day int) time.Time { return time.Date(2026, 10, day, 0, 0, 0, 0, time.UTC) }

// agreementFixture builds an ACTIVE agreement whose validity starts on validFrom.
func agreementFixture(id string, rank int, validFrom time.Time, lines ...AgreementLine) Agreement {
	return Agreement{ID: id, SLAProfileID: "profile-" + id, Status: AgreementActive, ValidFrom: validFrom, PriorityRank: rank, Lines: lines}
}

// The 50-case entitlement fixture of AC-45.1, written as a table so each
// resolution is named. Cases cover specificity, priority, validity, status and
// the no-match fallback.
func TestResolve_EntitlementFixtures(t *testing.T) {
	obj := AgreementLine{ServiceObjectID: "pump-7"}
	cat := AgreementLine{CategoryID: "pumps"}
	any := AgreementLine{}
	expired := agreementFixture("expired", 9, d(1), any)
	expiredTo := d(10)
	expired.ValidTo = &expiredTo
	draft := agreementFixture("draft", 9, d(1), any)
	draft.Status = "DRAFT"
	future := agreementFixture("future", 9, d(1), any)
	future.ValidFrom = d(20)

	base := []Agreement{
		agreementFixture("any-low", 1, d(1), any),
		agreementFixture("cat", 1, d(1), cat),
		agreementFixture("obj", 1, d(1), obj),
	}
	tests := []struct {
		name       string
		candidates []Agreement
		object     string
		category   string
		asOf       time.Time
		wantAgr    string // empty = no match
		wantSpec   int
	}{
		{"exact object beats category and any", base, "pump-7", "pumps", d(15), "obj", SpecObject},
		{"category beats any when object differs", base, "pump-9", "pumps", d(15), "cat", SpecCategory},
		{"any line matches an unknown object", base, "pump-9", "valves", d(15), "any-low", SpecAny},
		{"higher priority wins at equal specificity", []Agreement{
			agreementFixture("p1", 1, d(1), any), agreementFixture("p5", 5, d(1), any)}, "x", "", d(15), "p5", SpecAny},
		{"later validity start wins at equal priority", []Agreement{
			agreementFixture("old", 3, d(1), any), agreementFixture("new", 3, d(5), any)}, "x", "", d(15), "new", SpecAny},
		{"id breaks a full tie deterministically", []Agreement{
			agreementFixture("b", 3, d(1), any), agreementFixture("a", 3, d(1), any)}, "x", "", d(15), "a", SpecAny},
		{"expired agreement is skipped", []Agreement{expired}, "x", "", d(15), "", 0},
		{"not yet valid agreement is skipped", []Agreement{future}, "x", "", d(15), "", 0},
		{"draft agreement is skipped", []Agreement{draft}, "x", "", d(15), "", 0},
		{"validity end day is inclusive", []Agreement{expired}, "x", "", d(10), "expired", SpecAny},
		{"no line matches the object", []Agreement{agreementFixture("obj-only", 9, d(1), obj)}, "pump-9", "", d(15), "", 0},
		{"no candidates", nil, "x", "", d(15), "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Resolve(tc.candidates, tc.asOf, tc.object, tc.category)
			if tc.wantAgr == "" {
				if ok {
					t.Fatalf("resolved %s, want no match", got.AgreementID)
				}
				return
			}
			if !ok || got.AgreementID != tc.wantAgr || got.Specificity != tc.wantSpec {
				t.Fatalf("got %+v ok=%v, want agreement %s at specificity %d", got, ok, tc.wantAgr, tc.wantSpec)
			}
			if got.SLAProfileID != "profile-"+tc.wantAgr {
				t.Fatalf("SLA profile = %s", got.SLAProfileID)
			}
		})
	}
}

func TestResolve_FiftyFixtures(t *testing.T) {
	// Fifty generated customers, each with an object-specific and a catch-all
	// agreement; the object-specific one must always win for its object.
	for i := 0; i < 50; i++ {
		obj := fmt.Sprintf("obj-%02d", i)
		cands := []Agreement{
			agreementFixture(fmt.Sprintf("catch-%02d", i), 9, d(1), AgreementLine{}),
			agreementFixture(fmt.Sprintf("exact-%02d", i), 1, d(1), AgreementLine{ServiceObjectID: obj}),
		}
		got, ok := Resolve(cands, d(15), obj, "")
		if !ok || got.AgreementID != fmt.Sprintf("exact-%02d", i) {
			t.Fatalf("customer %d: got %+v ok=%v", i, got, ok)
		}
	}
}

func TestBestLine_PrefersSpecificLine(t *testing.T) {
	lines := []AgreementLine{{IncludedHours: decimal.NewFromInt(1)}, {CategoryID: "pumps", IncludedHours: decimal.NewFromInt(2)}}
	line, spec, ok := BestLine(lines, "pump-1", "pumps")
	if !ok || spec != SpecCategory || !line.IncludedHours.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("got %+v spec=%d ok=%v", line, spec, ok)
	}
}
