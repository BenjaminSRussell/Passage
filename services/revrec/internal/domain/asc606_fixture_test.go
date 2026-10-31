package domain

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// asc606Scenario is one bundle of testdata/asc606-40.json (§5.29.9 fixture
// asc606-40). The expected amounts come from testdata/gen_asc606_40.py, an
// exact rational reference that shares no code with this package.
type asc606Scenario struct {
	ID               string `json:"id"`
	Description      string `json:"description"`
	Currency         string `json:"currency"`
	TransactionPrice string `json:"transactionPrice"`
	ResidualRule     string `json:"residualRule"`
	RatableBasis     string `json:"ratableBasis"`
	ContractDate     string `json:"contractDate"`
	AsOf             string `json:"asOf"`
	Obligations      []struct {
		ID          string `json:"id"`
		SSP         string `json:"ssp"`
		SSPSource   string `json:"sspSource"`
		Method      string `json:"method"`
		TermStart   string `json:"termStart"`
		TermEnd     string `json:"termEnd"`
		SatisfiedOn string `json:"satisfiedOn"`
	} `json:"obligations"`
	Expected struct {
		Allocated []string `json:"allocated"`
		Schedules [][]struct {
			Date   *string `json:"date"`
			Amount string  `json:"amount"`
		} `json:"schedules"`
		RecognizedAsOf    string `json:"recognizedAsOf"`
		DeferredAdditions string `json:"deferredAdditions"`
		DeferredAsOf      string `json:"deferredAsOf"`
	} `json:"expected"`
}

// loadASC606Fixture reads the 40 scenarios from path.
func loadASC606Fixture(t testing.TB, path string) []asc606Scenario {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Scenarios []asc606Scenario `json:"scenarios"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Scenarios) != 40 {
		t.Fatalf("fixture has %d scenarios, want 40", len(fx.Scenarios))
	}
	return fx.Scenarios
}

func optDate(t testing.TB, s string) *time.Time {
	t.Helper()
	if s == "" {
		return nil
	}
	v, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatal(err)
	}
	return &v
}

// TestASC606Fixture40_AllocateAndSchedule replays every scenario through the
// engine the API uses (CheckSSPSources, Allocate, ScheduleLines) and checks the
// allocation, each schedule line's date and amount, and the revenue recognized
// and still deferred at the scenario's as-of month end against the reference.
func TestASC606Fixture40_AllocateAndSchedule(t *testing.T) {
	for _, sc := range loadASC606Fixture(t, "testdata/asc606-40.json") {
		t.Run(sc.ID, func(t *testing.T) {
			asOf := *optDate(t, sc.AsOf)
			obs := make([]Obligation, len(sc.Obligations))
			for i, o := range sc.Obligations {
				obs[i] = Obligation{ID: o.ID, SSP: d(o.SSP), SSPSource: o.SSPSource, Method: o.Method}
			}
			if err := CheckSSPSources(obs); err != nil {
				t.Fatal(err)
			}
			allocated, err := Allocate(money.Amount{Value: d(sc.TransactionPrice), Currency: sc.Currency}, obs, sc.ResidualRule)
			if err != nil {
				t.Fatal(err)
			}
			recognized, deferred := decimal.Zero, decimal.Zero
			for i, o := range allocated {
				in := sc.Obligations[i]
				if !o.Allocated.Equal(d(sc.Expected.Allocated[i])) {
					t.Errorf("%s allocated %s, want %s", o.ID, o.Allocated, sc.Expected.Allocated[i])
				}
				lines, err := ScheduleLines(o, sc.Currency, sc.RatableBasis, optDate(t, in.TermStart), optDate(t, in.TermEnd), optDate(t, in.SatisfiedOn))
				if err != nil {
					t.Fatal(err)
				}
				want := sc.Expected.Schedules[i]
				if len(lines) != len(want) {
					t.Fatalf("%s has %d schedule lines, want %d", o.ID, len(lines), len(want))
				}
				for j, l := range lines {
					gotDate := ""
					if l.Date != nil {
						gotDate = l.Date.Format("2006-01-02")
					}
					wantDate := ""
					if want[j].Date != nil {
						wantDate = *want[j].Date
					}
					if gotDate != wantDate || !l.Amount.Equal(d(want[j].Amount)) {
						t.Errorf("%s line %d: %q %s, want %q %s", o.ID, j+1, gotDate, l.Amount, wantDate, want[j].Amount)
					}
					due := l.Date != nil && !l.Date.After(asOf)
					if o.Method == MethodMilestone {
						sat := optDate(t, in.SatisfiedOn)
						due = sat != nil && !sat.After(asOf)
					}
					if due {
						recognized = recognized.Add(l.Amount)
					} else if o.Method != MethodPointInTime {
						deferred = deferred.Add(l.Amount)
					}
				}
			}
			if !recognized.Equal(d(sc.Expected.RecognizedAsOf)) || !deferred.Equal(d(sc.Expected.DeferredAsOf)) {
				t.Errorf("as of %s recognized %s deferred %s, want %s and %s", sc.AsOf, recognized, deferred, sc.Expected.RecognizedAsOf, sc.Expected.DeferredAsOf)
			}
		})
	}
}
