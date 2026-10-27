package domain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// atpFixture is one case of testdata/atp-100.json (PRD §5.27.9 "atp-100"). The
// expected promises were computed by an oracle written separately from this
// package (integer arithmetic over the BR-27.1 definitions); the first nine
// cases are the worked examples of BR-27.1, AC-27.1 and AC-27.8 and the
// bucket-boundary edge cases.
type atpFixture struct {
	ID           string `json:"id"`
	Note         string `json:"note"`
	Requested    string `json:"requested"`
	Quantity     string `json:"quantity"`
	CompleteOnly bool   `json:"completeOnly"`
	Periods      []struct {
		Date   string `json:"date"`
		Supply string `json:"supply"`
		Demand string `json:"demand"`
	} `json:"periods"`
	Expect struct {
		Complete     bool    `json:"complete"`
		PromisedDate *string `json:"promisedDate"`
		Backorder    string  `json:"backorder"`
		Schedule     []struct {
			Date     string `json:"date"`
			Quantity string `json:"quantity"`
			Period   int    `json:"period"`
		} `json:"schedule"`
	} `json:"expect"`
}

func mustDay(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func loadATPFixtures(t *testing.T) []atpFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "atp-100.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out []atpFixture
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestATPScenarios_100 is the PRD's "ATP scenarios" test (100/100 expected
// dates) and AC-27.1: every fixture's promise, schedule and backorder match.
func TestATPScenarios_100(t *testing.T) {
	fixtures := loadATPFixtures(t)
	if len(fixtures) != 100 {
		t.Fatalf("want 100 fixtures, got %d", len(fixtures))
	}
	for _, fx := range fixtures {
		t.Run(fx.ID, func(t *testing.T) {
			periods := make([]Period, len(fx.Periods))
			for i, p := range fx.Periods {
				periods[i] = Period{Date: mustDay(t, p.Date), Supply: decimal.RequireFromString(p.Supply), Demand: decimal.RequireFromString(p.Demand)}
			}
			got, err := PromiseDate(periods, mustDay(t, fx.Requested), decimal.RequireFromString(fx.Quantity), fx.CompleteOnly)
			if err != nil {
				t.Fatal(err)
			}
			want := fx.Expect
			if got.Complete != want.Complete || !got.Backorder.Equal(decimal.RequireFromString(want.Backorder)) {
				t.Fatalf("%s: complete=%v backorder=%s; want complete=%v backorder=%s", fx.Note, got.Complete, got.Backorder, want.Complete, want.Backorder)
			}
			if want.PromisedDate == nil {
				if !got.PromisedDate.IsZero() {
					t.Fatalf("%s: promised %s, want no date", fx.Note, got.PromisedDate)
				}
			} else if !got.PromisedDate.Equal(mustDay(t, *want.PromisedDate)) {
				t.Fatalf("%s: promised %s want %s", fx.Note, got.PromisedDate.Format("2006-01-02"), *want.PromisedDate)
			}
			if len(got.Schedule) != len(want.Schedule) {
				t.Fatalf("%s: schedule %+v want %+v", fx.Note, got.Schedule, want.Schedule)
			}
			for i, s := range want.Schedule {
				g := got.Schedule[i]
				if g.Period != s.Period || !g.Date.Equal(mustDay(t, s.Date)) || !g.Quantity.Equal(decimal.RequireFromString(s.Quantity)) {
					t.Fatalf("%s: schedule[%d] = %+v want %+v", fx.Note, i, g, s)
				}
			}
			if got.Source != SourceATP {
				t.Fatalf("source %s", got.Source)
			}
		})
	}
}
