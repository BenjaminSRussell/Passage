package domain

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

type referenceTxn struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Qty   string `json:"qty"`
	Value string `json:"value"`
	Date  string `json:"date"`
}

type referenceExpectation struct {
	Issues         map[string]string `json:"issues"`
	RemainingQty   string            `json:"remainingQty"`
	RemainingValue string            `json:"remainingValue"`
}

type referenceSequence struct {
	ID               string                          `json:"id"`
	StandardUnitCost string                          `json:"standardUnitCost"`
	Transactions     []referenceTxn                  `json:"transactions"`
	Expected         map[string]referenceExpectation `json:"expected"`
}

func (s referenceSequence) txns(t *testing.T) []Txn {
	t.Helper()
	out := make([]Txn, 0, len(s.Transactions))
	for _, r := range s.Transactions {
		day, err := time.Parse("2006-01-02", r.Date)
		if err != nil {
			t.Fatal(err)
		}
		tx := Txn{ID: r.ID, Kind: Receipt, Qty: dec(r.Qty), FinancialDate: day}
		if r.Kind == "I" {
			tx.Kind = Issue
		} else {
			tx.Value = dec(r.Value)
		}
		out = append(out, tx)
	}
	return out
}

func TestSettle_ReferenceFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/cost_reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Scale     int32               `json:"scale"`
		Sequences []referenceSequence `json:"sequences"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Sequences) != 100 {
		t.Fatalf("fixture holds %d sequences, want 100", len(fixture.Sequences))
	}
	methods := []Method{FIFO, LIFO, LIFODate, WeightedAvgPeriodic, WeightedAvgRunning, MovingAvg, Standard}
	for _, seq := range fixture.Sequences {
		txns := seq.txns(t)
		for _, m := range methods {
			want, ok := seq.Expected[string(m)]
			if !ok {
				t.Fatalf("%s has no expectation for %s", seq.ID, m)
			}
			res, err := Settle(txns, Options{Method: m, Scale: fixture.Scale, Standard: dec(seq.StandardUnitCost)})
			if err != nil {
				t.Fatalf("%s %s: %v", seq.ID, m, err)
			}
			if len(res.Exceptions) != 0 || len(res.IssueCosts) != len(want.Issues) {
				t.Errorf("%s %s: %d issue costs and %d exceptions, want %d and none", seq.ID, m, len(res.IssueCosts), len(res.Exceptions), len(want.Issues))
			}
			for id, cost := range want.Issues {
				if got := res.IssueCosts[id].StringFixed(2); got != cost {
					t.Errorf("%s %s issue %s = %s, want %s", seq.ID, m, id, got, cost)
				}
			}
			if got := res.RemainingValue.StringFixed(2); got != want.RemainingValue {
				t.Errorf("%s %s remaining value = %s, want %s", seq.ID, m, got, want.RemainingValue)
			}
			if got := res.RemainingQty.String(); got != want.RemainingQty {
				t.Errorf("%s %s remaining qty = %s, want %s", seq.ID, m, got, want.RemainingQty)
			}
		}
	}
}
