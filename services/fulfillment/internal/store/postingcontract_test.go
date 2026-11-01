package store_test

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// matrixCell is one cell of testdata/cogs-matrix-14.json (independent reference
// testdata/gen_cogs_matrix14.py).
type matrixCell struct {
	ID             string `json:"id"`
	CogsAtShipment bool   `json:"cogsAtShipment"`
	Currency       string `json:"currency"`
	Lines          []struct {
		OrderLineID string `json:"orderLineId"`
		Kind        string `json:"kind"`
		Qty         string `json:"qty"`
		UnitCost    string `json:"unitCost"`
	} `json:"lines"`
	Journal []struct {
		Account     string `json:"account"`
		Amount      string `json:"amount"`
		OrderLineID string `json:"orderLineId"`
	} `json:"journal"`
	TotalCost  string `json:"totalCost"`
	LedgerPost []struct {
		Account       string `json:"account"`
		DebitCredit   string `json:"debitCredit"`
		Amount        string `json:"amount"`
		SourceLineRef string `json:"sourceLineRef"`
	} `json:"ledgerPost"`
}

// ledgerKey is one ledger PostLine as the ledger API takes it: a side and a
// non-negative amount (services/ledger api/openapi.yaml PostLine).
type ledgerKey struct{ account, side, amount, line string }

// ledgerLines maps an event journal onto ledger PostLines and checks the ledger's
// validation (LEDGER_CURRENCY_MISMATCH, LEDGER_AMOUNT_SCALE, LEDGER_LINE_EMPTY,
// LEDGER_UNBALANCED); line names the fixture order line of each real one.
func ledgerLines(t *testing.T, journal []fulevents.JournalEntry, currency string, line map[string]string) []ledgerKey {
	t.Helper()
	minor := money.MinorUnits(currency)
	debit, credit := decimal.Zero, decimal.Zero
	out := []ledgerKey{}
	for i, j := range journal {
		v, err := decimal.NewFromString(j.Amount)
		switch {
		case err != nil:
			t.Fatalf("journal[%d] amount %q: %v", i, j.Amount, err)
		case j.Currency != currency:
			t.Fatalf("journal[%d] currency %s, slip %s", i, j.Currency, currency)
		case !v.Equal(v.Round(minor)):
			t.Fatalf("journal[%d] amount %s has more than %d decimals", i, v, minor)
		case v.IsZero():
			t.Fatalf("journal[%d] has no amount", i)
		}
		side := "D"
		if v.IsNegative() {
			side, credit = "C", credit.Add(v.Neg())
		} else {
			debit = debit.Add(v)
		}
		out = append(out, ledgerKey{j.Account, side, v.Abs().StringFixed(minor), line[j.OrderLineID]})
	}
	if !debit.Equal(credit) {
		t.Fatalf("debits %s, credits %s", debit, credit)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].line != out[b].line {
			return out[a].line < out[b].line
		}
		return out[a].account < out[b].account
	})
	return out
}

func shipmentEvents[T any](t *testing.T, admin *pgxpool.Pool, shipmentID, eventType string) []T {
	t.Helper()
	rows, err := admin.Query(context.Background(), `select payload->'data' from outbox where aggregate_id = $1 and type = $2 order by created_at, id`, shipmentID, eventType)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var raw []byte
		var v T
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

// TestPackingSlipLedgerContract_CogsMatrix runs every cogs-matrix-14 cell that
// has stocked lines (the store posts stocked lines only: 10 of the 14 cells;
// drop-ship and non-stocked lines are not modelled at the store, their two
// scenarios stay domain-only) through release, pick, pack, post, cancel and
// re-post against Postgres, and checks the contract the ledger consumes: the
// packingslip.posted journal is exactly the reference journal as valid,
// balanced ledger PostLines; its lines carry the reference cost; the
// packingslip.cancelled journal and lines are the exact mirror, so every
// account nets to zero; and the re-post gives the original journal again.
func TestPackingSlipLedgerContract_CogsMatrix(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/cogs-matrix-14.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Cells []matrixCell `json:"cells"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	s, admin := setup(t)
	ctx := ctxFor(28, "")
	ran := 0
	for _, c := range m.Cells {
		var stocked []int
		for i, l := range c.Lines {
			if l.Kind == string(domain.KindStocked) {
				stocked = append(stocked, i)
			}
		}
		if len(stocked) == 0 {
			continue
		}
		ran++
		t.Run(c.ID, func(t *testing.T) {
			order, customer, le, wh := ids.New(), ids.New(), ids.New(), ids.New()
			label := map[string]string{} // real order line id -> fixture order line
			unit := map[string]string{}  // real order line id -> unit cost
			var rl []domain.ReleaseLine
			for _, i := range stocked {
				l := c.Lines[i]
				ol := ids.New()
				label[ol], unit[ol] = l.OrderLineID, l.UnitCost
				rl = append(rl, domain.ReleaseLine{OrderID: order, OrderLineID: ol, CustomerID: customer, LegalEntityID: le, WarehouseID: wh,
					ProductID: ids.New(), Unit: "EA", Qty: dq(l.Qty), ConfirmedShipDate: today(), ATPOK: true})
			}
			groups, _ := domain.Release(rl, domain.ReleaseRule{Today: today(), HorizonDays: 2, RequireFullATP: true})
			res, err := s.RunRelease(ctx, wh, groups, idem("rel"))
			if err != nil || len(res.Shipments) != 1 {
				t.Fatalf("RunRelease: %+v, %v", res, err)
			}
			sh := res.Shipments[0]
			cost := map[string]store.Cost{}
			for _, l := range sh.Lines {
				cost[l.ID] = store.Cost{UnitCost: dq(unit[l.OrderLineID]), CostSnapshotID: "snap-" + label[l.OrderLineID]}
			}
			setCogsPolicy(t, s, ctx, le, c.CogsAtShipment)
			in := store.PostInput{PostingDate: today(), Currency: c.Currency, CogsAtShipment: c.CogsAtShipment, Costs: cost}
			packed := pickAndPack(t, s, ctx, sh)
			slip, err := s.PostPackingSlip(ctx, sh.ID, packed.Shipment.Version, in, idem("post"))
			if err != nil {
				t.Fatal(err)
			}

			want := []ledgerKey{}
			for _, p := range c.LedgerPost {
				want = append(want, ledgerKey{p.Account, p.DebitCredit, p.Amount, p.SourceLineRef})
			}
			sort.Slice(want, func(a, b int) bool {
				if want[a].line != want[b].line {
					return want[a].line < want[b].line
				}
				return want[a].account < want[b].account
			})
			posted := shipmentEvents[fulevents.PackingSlipPostedData](t, admin, sh.ID, fulevents.PackingSlipPosted)
			if len(posted) != 1 || posted[0].PackingSlipID != slip.ID {
				t.Fatalf("posted events = %+v, want one for %s", posted, slip.ID)
			}
			got := ledgerLines(t, posted[0].Journal, c.Currency, label)
			if len(got) != len(want) {
				t.Fatalf("ledger post %+v, want %+v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("ledger post[%d] = %+v, want %+v", i, got[i], want[i])
				}
			}
			if !dq(posted[0].TotalCost).Equal(dq(c.TotalCost)) || !slip.TotalCost.Equal(dq(c.TotalCost)) {
				t.Fatalf("total cost event %s slip %s, want %s", posted[0].TotalCost, slip.TotalCost, c.TotalCost)
			}
			// Per line: the posted cost is the credit to 1400 of that order line.
			lineCost := map[string]decimal.Decimal{}
			for _, j := range c.Journal {
				if j.Account == domain.AccountInventory {
					lineCost[j.OrderLineID] = dq(j.Amount).Neg()
				}
			}
			for _, l := range posted[0].Lines {
				if !dq(l.CostAmount).Equal(lineCost[label[l.OrderLineID]]) {
					t.Fatalf("line %s cost_amount %s, want %s", label[l.OrderLineID], l.CostAmount, lineCost[label[l.OrderLineID]])
				}
			}

			rev, err := s.CancelPackingSlip(ctx, slip.ID, idem("cancel"))
			if err != nil {
				t.Fatal(err)
			}
			cancelled := shipmentEvents[fulevents.PackingSlipCancelledData](t, admin, sh.ID, fulevents.PackingSlipCancelled)
			if len(cancelled) != 1 || cancelled[0].PackingSlipID != slip.ID || cancelled[0].ReversalID != rev.ID {
				t.Fatalf("cancelled events = %+v", cancelled)
			}
			mirror := ledgerLines(t, cancelled[0].Journal, c.Currency, label)
			if len(mirror) != len(want) {
				t.Fatalf("ledger cancel has %d lines, want %d", len(mirror), len(want))
			}
			for i, w := range want {
				flip := map[string]string{"D": "C", "C": "D"}[w.side]
				if mirror[i] != (ledgerKey{w.account, flip, w.amount, w.line}) {
					t.Fatalf("ledger cancel[%d] = %+v, want the mirror of %+v", i, mirror[i], w)
				}
			}
			net := map[string]decimal.Decimal{}
			for _, j := range append(append([]fulevents.JournalEntry{}, posted[0].Journal...), cancelled[0].Journal...) {
				net[j.Account] = net[j.Account].Add(dq(j.Amount))
			}
			for acct, v := range net {
				if !v.IsZero() {
					t.Fatalf("account %s nets %s after the cancellation", acct, v)
				}
			}
			postedQty := map[string]decimal.Decimal{}
			for _, l := range posted[0].Lines {
				postedQty[l.OrderLineID] = dq(l.Qty)
			}
			for _, l := range cancelled[0].Lines {
				if !dq(l.Qty).Equal(postedQty[l.OrderLineID].Neg()) || !dq(l.CostAmount).Equal(lineCost[label[l.OrderLineID]].Neg()) {
					t.Fatalf("reversal line %s qty %s cost %s, want the negated %s and %s", label[l.OrderLineID], l.Qty, l.CostAmount,
						postedQty[l.OrderLineID], lineCost[label[l.OrderLineID]])
				}
			}
			if len(cancelled[0].Lines) != len(posted[0].Lines) {
				t.Fatalf("reversal has %d lines, the slip %d", len(cancelled[0].Lines), len(posted[0].Lines))
			}

			after := getShipment(t, s, ctx, sh.ID)
			if _, err := s.PostPackingSlip(ctx, sh.ID, after.Version, in, idem("repost")); err != nil {
				t.Fatalf("repost: %v", err)
			}
			reposted := shipmentEvents[fulevents.PackingSlipPostedData](t, admin, sh.ID, fulevents.PackingSlipPosted)
			if len(reposted) != 2 {
				t.Fatalf("posted events after the re-post = %d, want 2", len(reposted))
			}
			again := ledgerLines(t, reposted[1].Journal, c.Currency, label)
			for i := range want {
				if again[i] != want[i] {
					t.Fatalf("re-post ledger[%d] = %+v, want %+v", i, again[i], want[i])
				}
			}
		})
	}
	if ran != 10 {
		t.Fatalf("ran %d store cells, want the 10 with stocked lines", ran)
	}
}
