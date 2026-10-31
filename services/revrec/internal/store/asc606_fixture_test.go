package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/domain"
	rev "github.com/BenjaminSRussell/passage/services/revrec/internal/events"
)

// asc606Scenario is the part of ../domain/testdata/asc606-40.json this test
// replays; the expected amounts come from the independent reference
// ../domain/testdata/gen_asc606_40.py.
type asc606Scenario struct {
	ID               string `json:"id"`
	Currency         string `json:"currency"`
	TransactionPrice string `json:"transactionPrice"`
	ResidualRule     string `json:"residualRule"`
	RatableBasis     string `json:"ratableBasis"`
	ContractDate     string `json:"contractDate"`
	AsOf             string `json:"asOf"`
	Obligations      []struct {
		SSP         string `json:"ssp"`
		SSPSource   string `json:"sspSource"`
		Method      string `json:"method"`
		TermStart   string `json:"termStart"`
		TermEnd     string `json:"termEnd"`
		SatisfiedOn string `json:"satisfiedOn"`
	} `json:"obligations"`
	Expected struct {
		Rejected       string `json:"rejected"`
		ClosingByMonth []struct {
			Month   string `json:"month"`
			Closing string `json:"closing"`
		} `json:"closingByMonth"`
		RecognizedAsOf    string `json:"recognizedAsOf"`
		DeferredAdditions string `json:"deferredAdditions"`
		DeferredAsOf      string `json:"deferredAsOf"`
	} `json:"expected"`
}

func parseDay(t *testing.T, s string) *time.Time {
	t.Helper()
	if s == "" {
		return nil
	}
	v, err := time.Parse(dateLayout, s)
	if err != nil {
		t.Fatal(err)
	}
	return &v
}

// scenarioContract builds the contract the API would create for a scenario.
func scenarioContract(t *testing.T, sc asc606Scenario, legalEntityID string) Contract {
	t.Helper()
	obs := make([]domain.Obligation, len(sc.Obligations))
	for i, o := range sc.Obligations {
		obs[i] = domain.Obligation{ID: ids.New(), SSP: decimal.RequireFromString(o.SSP), SSPSource: o.SSPSource, Method: o.Method}
	}
	if err := domain.CheckSSPSources(obs); err != nil {
		t.Fatal(err)
	}
	priceAmount := money.Amount{Value: decimal.RequireFromString(sc.TransactionPrice), Currency: sc.Currency}.Round()
	price := priceAmount.Value
	allocated, err := domain.Allocate(priceAmount, obs, sc.ResidualRule)
	if err != nil {
		t.Fatal(err)
	}
	c := Contract{
		ID: ids.New(), LegalEntityID: legalEntityID, OrderID: ids.New(), CustomerID: ids.New(), Currency: sc.Currency,
		TransactionPrice: price, ResidualRule: sc.ResidualRule, RatableBasis: sc.RatableBasis,
		ContractDate: *parseDay(t, sc.ContractDate), IdempotencyKey: ids.New(), RequestHash: "hash",
	}
	for i, o := range allocated {
		in := sc.Obligations[i]
		ob := Obligation{ID: o.ID, ContractID: c.ID, LineNo: i + 1, ProductID: ids.New(), SSP: o.SSP, SSPSource: o.SSPSource,
			Allocated: o.Allocated, Method: o.Method, TermStart: parseDay(t, in.TermStart), TermEnd: parseDay(t, in.TermEnd)}
		if o.Method == domain.MethodPointInTime {
			ob.SatisfiedOn = parseDay(t, in.SatisfiedOn)
		}
		lines, err := domain.ScheduleLines(o, sc.Currency, sc.RatableBasis, ob.TermStart, ob.TermEnd, ob.SatisfiedOn)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range lines {
			s := Schedule{ID: ids.New(), Seq: l.Seq, Date: l.Date, Amount: l.Amount, Status: domain.InitialStatus(o.Method, l.Date)}
			if s.Status == domain.ScheduleRecognized {
				s.RecognizedOn = l.Date
			}
			ob.Schedule = append(ob.Schedule, s)
		}
		c.Obligations = append(c.Obligations, ob)
	}
	return c
}

// gl2400 is the deferred revenue account a ledger fed only by revrec's events
// would hold for one legal entity at the end of each day: contract.allocated
// credits the RATABLE and MILESTONE allocations on the contract date (the
// invoice's deferred share, FR-29.9) and every schedule.recognized debits its
// amount from its deferred account on its recognition date.
type gl2400 struct {
	credits map[string]decimal.Decimal // contract date -> amount
	debits  map[string]decimal.Decimal // recognized on -> amount
}

func loadGL2400(t *testing.T, pool *pgxpool.Pool, legalEntityID string) gl2400 {
	t.Helper()
	deferredByMethod := map[string]bool{"RATABLE": true, "MILESTONE": true}
	g := gl2400{credits: map[string]decimal.Decimal{}, debits: map[string]decimal.Decimal{}}
	rows, err := pool.Query(context.Background(), `select type, payload->'data' from outbox
		where type = any($1::text[]) and payload->'data'->>'legal_entity_id' = $2`,
		[]string{rev.ContractAllocated, rev.ScheduleRecognized}, legalEntityID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var typ string
		var data []byte
		if err := rows.Scan(&typ, &data); err != nil {
			t.Fatal(err)
		}
		switch typ {
		case rev.ContractAllocated:
			var a rev.ContractAllocatedData
			if err := json.Unmarshal(data, &a); err != nil {
				t.Fatal(err)
			}
			for _, o := range a.Obligations {
				if deferredByMethod[o.Method] {
					g.credits[a.ContractDate] = g.credits[a.ContractDate].Add(decimal.RequireFromString(o.Allocated))
				}
			}
		case rev.ScheduleRecognized:
			var r rev.ScheduleRecognizedData
			if err := json.Unmarshal(data, &r); err != nil {
				t.Fatal(err)
			}
			if r.DeferredAccount != "2400" || r.RevenueAccount != "4000" {
				t.Errorf("schedule.recognized posts Dr %s / Cr %s, want Dr 2400 / Cr 4000", r.DeferredAccount, r.RevenueAccount)
			}
			g.debits[r.RecognizedOn] = g.debits[r.RecognizedOn].Add(decimal.RequireFromString(r.Amount))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return g
}

// balance is the credit balance of 2400 at the end of `through`.
func (g gl2400) balance(through time.Time) decimal.Decimal {
	end := through.Format(dateLayout)
	out := decimal.Zero
	for d, v := range g.credits {
		if d <= end {
			out = out.Add(v)
		}
	}
	for d, v := range g.debits {
		if d <= end {
			out = out.Sub(v)
		}
	}
	return out
}

// TestASC606Fixture40_RecognizeAndReconcileGL2400 replays the 40 bundle
// scenarios of the asc606-40 fixture through Postgres: each contract is
// created, its milestones satisfied on their dates up to the as-of month end,
// and a recognition run is made for every month end from the contract month to
// the as-of date. Then, per scenario:
//   - the contract's recognized revenue and the roll-forward's additions and
//     closing balance equal the independent reference (AC-29.3 engineering
//     reference; the accountant-reviewed workbook is still open);
//   - every month's roll-forward opens at the previous month's closing and
//     closes at the 2400 balance of a ledger fed only by the outbox events
//     (issue #29 Proof 3 against an event-derived GL; the live ledger is a handoff).
func TestASC606Fixture40_RecognizeAndReconcileGL2400(t *testing.T) {
	raw, err := os.ReadFile("../domain/testdata/asc606-40.json")
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
	s, pool := testStore(t)
	ctx := tenantCtx(1, "")
	for _, sc := range fx.Scenarios {
		t.Run(sc.ID, func(t *testing.T) {
			if sc.Expected.Rejected != "" {
				t.Skipf("rejected with %s, covered by the domain fixture test", sc.Expected.Rejected)
			}
			le := ids.New()
			created, err := s.CreateContract(ctx, scenarioContract(t, sc, le))
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			asOf := *parseDay(t, sc.AsOf)
			for i, o := range sc.Obligations {
				on := parseDay(t, o.SatisfiedOn)
				if o.Method != domain.MethodMilestone || on == nil || on.After(asOf) {
					continue
				}
				if _, err := s.SatisfyMilestone(ctx, created.Obligations[i].ID, *on, 0); err != nil {
					t.Fatalf("satisfy obligation %d on %s: %v", i+1, o.SatisfiedOn, err)
				}
			}
			first := time.Date(created.ContractDate.Year(), created.ContractDate.Month(), 1, 0, 0, 0, 0, time.UTC)
			var months []time.Time
			for m := first; !m.After(asOf); m = m.AddDate(0, 1, 0) {
				months = append(months, m)
				if _, _, err := s.RunRecognition(ctx, le, m.AddDate(0, 1, -1)); err != nil {
					t.Fatalf("run %s: %v", m.Format("2006-01"), err)
				}
			}

			got, err := s.GetContract(ctx, created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if want := decimal.RequireFromString(sc.Expected.RecognizedAsOf); !got.Recognized.Equal(want) {
				t.Errorf("recognized %s, want %s", got.Recognized, want)
			}
			total, err := s.RollForward(ctx, le, sc.Currency, first, asOf)
			if err != nil {
				t.Fatal(err)
			}
			if !total.Opening.IsZero() || !total.Additions.Equal(decimal.RequireFromString(sc.Expected.DeferredAdditions)) ||
				!total.Closing.Equal(decimal.RequireFromString(sc.Expected.DeferredAsOf)) {
				t.Errorf("roll-forward %s..%s %+v, want opening 0 additions %s closing %s",
					first.Format(dateLayout), sc.AsOf, total, sc.Expected.DeferredAdditions, sc.Expected.DeferredAsOf)
			}

			gl := loadGL2400(t, pool, le)
			prev := decimal.Zero
			if len(sc.Expected.ClosingByMonth) != len(months) {
				t.Fatalf("fixture has %d monthly closings, want %d", len(sc.Expected.ClosingByMonth), len(months))
			}
			for i, m := range months {
				end := m.AddDate(0, 1, -1)
				rf, err := s.RollForward(ctx, le, sc.Currency, m, end)
				if err != nil {
					t.Fatal(err)
				}
				if !rf.Opening.Equal(prev) {
					t.Errorf("%s opens at %s, want the previous closing %s", m.Format("2006-01"), rf.Opening, prev)
				}
				if bal := gl.balance(end); !rf.Closing.Equal(bal) {
					t.Errorf("%s roll-forward closes at %s, GL 2400 from events %s (diff %s)", m.Format("2006-01"), rf.Closing, bal, rf.Closing.Sub(bal))
				}
				want := sc.Expected.ClosingByMonth[i]
				if want.Month != m.Format("2006-01") || !rf.Closing.Equal(decimal.RequireFromString(want.Closing)) {
					t.Errorf("%s closes at %s, reference %s closes at %s", m.Format("2006-01"), rf.Closing, want.Month, want.Closing)
				}
				prev = rf.Closing
			}
		})
	}
}
