package domain_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
)

// AC-9.4: fixtures whose expected hours come from the independent oracle
// testdata/gen_routing_leadtime.py (which also re-derives the 12 fixtures in
// routing-leadtime.json): secondaries beside a delayed primary, overlap
// transfer batches, out-of-order numbering and a repeating-decimal efficiency.
func TestLeadTimeAdversarialFixtures(t *testing.T) {
	var fixtures []leadTimeFixture
	raw, err := os.ReadFile("../../testdata/routing-leadtime-adversarial.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no fixtures")
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

// Regression: a secondary used to start at hour 0 even when the primary it
// runs alongside (BR-9.7) waits for earlier operations, so its branch was
// hidden and the lead time came out at 7 h instead of 10 h.
func TestLeadTimeSecondaryStartsWithItsPrimary(t *testing.T) {
	ops := []domain.RouteOperation{
		op(10, 20, "", "1", "0.04"),              // 0 to 5 h
		op(20, 40, "", "0", "0.01"),              // primary, 5 to 6 h
		op(30, 40, "secondary1", "0.5", "0.035"), // alongside 20: 5 to 9 h
		op(40, 0, "", "1", "0"),                  // 9 to 10 h
	}
	got, err := domain.LeadTime(ops, dec("100"), dec("8"))
	if err != nil || !got.Hours.Equal(dec("10")) || got.CalendarDays != 2 {
		t.Fatalf("got %+v err %v, want 10 h / 2 d", got, err)
	}
}

func TestLeadTimeRejectsNegativeOverlap(t *testing.T) {
	o := op(10, 0, "", "1", "1")
	o.OverlapQty = dec("-1")
	if _, err := domain.LeadTime([]domain.RouteOperation{o}, dec("10"), dec("8")); !domain.HasCode(err, domain.CodeRouteQty) {
		t.Fatalf("err = %v, want %s", err, domain.CodeRouteQty)
	}
}

// Overlap only lets work start earlier, so it never lengthens a sequential
// route, and the route can never be shorter than its longest operation alone.
func TestLeadTimeOverlapNeverLengthensNorBeatsTheSlowestOperation(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(2, 5).Draw(t, "operations")
		qty := decimal.NewFromInt(int64(rapid.IntRange(1, 300).Draw(t, "qty")))
		plain := make([]domain.RouteOperation, n)
		overlapped := make([]domain.RouteOperation, n)
		for i := range plain {
			plain[i] = domain.RouteOperation{
				No: (i + 1) * 10, SetupH: decimal.NewFromInt(int64(rapid.IntRange(0, 4).Draw(t, "setup"))),
				RunH:         decimal.New(int64(rapid.IntRange(0, 50).Draw(t, "run")), -2),
				ProcessQty:   decimal.NewFromInt(int64(rapid.IntRange(1, 5).Draw(t, "pq"))),
				QueueBeforeH: decimal.New(int64(rapid.IntRange(0, 8).Draw(t, "qb")), -1),
				QueueAfterH:  decimal.New(int64(rapid.IntRange(0, 8).Draw(t, "qa")), -1),
				TransitH:     decimal.New(int64(rapid.IntRange(0, 8).Draw(t, "tr")), -1),
			}
			overlapped[i] = plain[i]
			overlapped[i].OverlapQty = decimal.NewFromInt(int64(rapid.IntRange(0, 320).Draw(t, "overlap")))
		}
		without, err1 := domain.LeadTime(plain, qty, dec("8"))
		with, err2 := domain.LeadTime(overlapped, qty, dec("8"))
		if err1 != nil || err2 != nil {
			t.Fatal(err1, err2)
		}
		if with.Hours.GreaterThan(without.Hours) {
			t.Fatalf("overlap lengthened the route: %s h > %s h", with.Hours, without.Hours)
		}
		for _, o := range plain {
			alone, err := domain.LeadTime([]domain.RouteOperation{o}, qty, dec("8"))
			if err != nil {
				t.Fatal(err)
			}
			if with.Hours.LessThan(alone.Hours) {
				t.Fatalf("route %s h is shorter than operation %d alone (%s h)", with.Hours, o.No, alone.Hours)
			}
		}
	})
}
