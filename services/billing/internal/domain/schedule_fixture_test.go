package domain

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// TestProrate_SubscriptionEdges25 runs the §5.29.9 fixture
// testdata/subscription-edges-25.json: mid-period start and cancel, start and
// cancel on or around the leap day, annual and quarterly periods with and
// without 29 February, exact half-unit ties, and JPY and KWD minor units. Each
// expected amount is hand-derived and re-checked by
// testdata/gen_subscription_edges_25.py from calendar day counts.
func TestProrate_SubscriptionEdges25(t *testing.T) {
	raw, err := os.ReadFile("testdata/subscription-edges-25.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Cases []struct {
			ID          string `json:"id"`
			Why         string `json:"why"`
			Amount      string `json:"amount"`
			Currency    string `json:"currency"`
			PeriodFrom  string `json:"periodFrom"`
			PeriodTo    string `json:"periodTo"`
			ServiceFrom string `json:"serviceFrom"`
			ServiceTo   string `json:"serviceTo"`
			Expected    string `json:"expected"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Cases) != 25 {
		t.Fatalf("fixture has %d cases, want 25", len(fx.Cases))
	}
	for _, c := range fx.Cases {
		t.Run(c.ID+" "+c.Why, func(t *testing.T) {
			got, err := Prorate(money.Amount{Value: d(c.Amount), Currency: c.Currency},
				day(c.PeriodFrom), day(c.PeriodTo), day(c.ServiceFrom), day(c.ServiceTo))
			if err != nil {
				t.Fatal(err)
			}
			if got.Currency != c.Currency || !got.Value.Equal(d(c.Expected)) {
				t.Errorf("got %s %s, want %s %s", got.Value, got.Currency, c.Expected, c.Currency)
			}
		})
	}
}
