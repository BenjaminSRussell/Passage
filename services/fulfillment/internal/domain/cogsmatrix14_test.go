package domain

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// cogsMatrix is testdata/cogs-matrix-14.json, written by the independent
// reference testdata/gen_cogs_matrix14.py (section 5.28.9 fixture "cogs-matrix-14").
type cogsMatrix struct {
	Cells []struct {
		ID             string `json:"id"`
		CogsAtShipment bool   `json:"cogsAtShipment"`
		Currency       string `json:"currency"`
		Lines          []struct {
			OrderLineID string `json:"orderLineId"`
			Kind        string `json:"kind"`
			Qty         string `json:"qty"`
			UnitCost    string `json:"unitCost"`
		} `json:"lines"`
		Journal      []fixtureEntry `json:"journal"`
		Mirror       []fixtureEntry `json:"mirror"`
		TotalCost    string         `json:"totalCost"`
		LedgerPost   []ledgerLine   `json:"ledgerPost"`
		LedgerCancel []ledgerLine   `json:"ledgerCancel"`
		LedgerTotal  string         `json:"ledgerTotal"`
	} `json:"cells"`
}

type fixtureEntry struct {
	Account     string `json:"account"`
	Amount      string `json:"amount"`
	OrderLineID string `json:"orderLineId"`
}

// ledgerLine is the ledger's PostLine (services/ledger api/openapi.yaml): a side
// and a non-negative amount, sourceLineRef naming the order line.
type ledgerLine struct {
	Account       string `json:"account"`
	DebitCredit   string `json:"debitCredit"`
	Amount        string `json:"amount"`
	SourceLineRef string `json:"sourceLineRef"`
}

// toLedgerPost maps a signed fulfillment journal onto the ledger PostEntryRequest
// lines and enforces the ledger's validation (ledger internal/domain
// ValidateEntry and api): every amount non-negative with its side, no more
// decimals than the currency allows, no zero line, debits equal credits.
func toLedgerPost(t *testing.T, journal []JournalLine, currency string) ([]ledgerLine, decimal.Decimal) {
	t.Helper()
	minor := money.MinorUnits(currency)
	out := []ledgerLine{}
	debit, credit := decimal.Zero, decimal.Zero
	for i, j := range journal {
		v := j.Amount.Value
		if j.Amount.Currency != currency {
			t.Fatalf("line %d currency %s, slip currency %s (LEDGER_CURRENCY_MISMATCH)", i, j.Amount.Currency, currency)
		}
		if !v.Equal(v.Round(minor)) {
			t.Fatalf("line %d amount %s has more than %d decimals (LEDGER_AMOUNT_SCALE)", i, v, minor)
		}
		if v.IsZero() {
			t.Fatalf("line %d has no amount (LEDGER_LINE_EMPTY)", i)
		}
		side := "D"
		if v.IsNegative() {
			side, credit = "C", credit.Add(v.Neg())
		} else {
			debit = debit.Add(v)
		}
		out = append(out, ledgerLine{Account: j.Account, DebitCredit: side, Amount: v.Abs().StringFixed(minor), SourceLineRef: j.OrderLineID})
	}
	if !debit.Equal(credit) {
		t.Fatalf("debits %s, credits %s (LEDGER_UNBALANCED)", debit, credit)
	}
	return out, debit
}

func sameEntries(t *testing.T, what string, got []JournalLine, want []fixtureEntry, currency string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s has %d lines, want %d: %+v", what, len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.Account != w.Account || g.OrderLineID != w.OrderLineID || g.Amount.Currency != currency || !g.Amount.Value.Equal(decimal.RequireFromString(w.Amount)) {
			t.Fatalf("%s[%d] = %s %s %s %s, want %s %s %s", what, i, g.Account, g.Amount.Value, g.Amount.Currency, g.OrderLineID, w.Account, w.Amount, w.OrderLineID)
		}
	}
}

func sameLedger(t *testing.T, what string, got, want []ledgerLine) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d ledger lines, want %d", what, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s[%d] = %+v, want %+v", what, i, got[i], want[i])
		}
	}
}

// TestCogsMatrix14_PostBalancedAndCancelMirrors is the "posting matrix" contract
// test of section 5.28.9 (14/14 balanced; cancel mirrors exactly): each of the
// seven BR-28.1 slip scenarios under both posting policies gives exactly the
// reference journal, is a valid balanced ledger PostEntryRequest, and its
// cancellation is the exact mirror (same accounts, order lines and currency,
// amounts negated, sides swapped), after which every account nets to zero.
func TestCogsMatrix14_PostBalancedAndCancelMirrors(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/cogs-matrix-14.json")
	if err != nil {
		t.Fatal(err)
	}
	var m cogsMatrix
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Cells) != 14 {
		t.Fatalf("fixture has %d cells, want 14", len(m.Cells))
	}
	passed := 0
	for _, c := range m.Cells {
		ok := t.Run(c.ID, func(t *testing.T) {
			var lines []PostingLine
			for _, l := range c.Lines {
				lines = append(lines, PostingLine{OrderLineID: l.OrderLineID, Kind: LineKind(l.Kind), Qty: decimal.RequireFromString(l.Qty),
					UnitCost: decimal.RequireFromString(l.UnitCost), CostSnapshotID: "snap-" + l.OrderLineID})
			}
			post, err := PackingSlipPosting(lines, c.CogsAtShipment, c.Currency)
			if err != nil {
				t.Fatal(err)
			}
			sameEntries(t, "journal", post, c.Journal, c.Currency)
			ledger, total := toLedgerPost(t, post, c.Currency)
			sameLedger(t, "ledger post", ledger, c.LedgerPost)
			if !total.Equal(decimal.RequireFromString(c.LedgerTotal)) {
				t.Fatalf("ledger total %s, want %s", total, c.LedgerTotal)
			}
			cost, err := Net(post, AccountInventory, c.Currency)
			if err != nil || !cost.Value.Neg().Equal(decimal.RequireFromString(c.TotalCost)) {
				t.Fatalf("Cr 1400 = %s, %v; want %s", cost.Value.Neg(), err, c.TotalCost)
			}

			mirror := Reverse(post)
			sameEntries(t, "mirror", mirror, c.Mirror, c.Currency)
			cancel, _ := toLedgerPost(t, mirror, c.Currency)
			sameLedger(t, "ledger cancel", cancel, c.LedgerCancel)
			both := append(append([]JournalLine{}, post...), mirror...)
			for _, acct := range []string{AccountInventory, AccountDeliveredNotInvoiced, AccountCOGS, AccountGRIRClearing} {
				if net, err := Net(both, acct, c.Currency); err != nil || !net.IsZero() {
					t.Fatalf("account %s nets %s after the cancellation, want 0 (%v)", acct, net.Value, err)
				}
			}
		})
		if ok {
			passed++
		}
	}
	t.Logf("%d/14 cells balanced with an exact cancellation mirror", passed)
}

// TestCogsMatrix14_FixtureMatchesReference: the committed fixture is exactly what
// the independent reference writes.
func TestCogsMatrix14_FixtureMatchesReference(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	out := filepath.Join(t.TempDir(), "cogs-matrix-14.json")
	if msg, err := exec.Command(py, "-I", "../../testdata/gen_cogs_matrix14.py", out).CombinedOutput(); err != nil {
		t.Fatalf("reference generator failed: %v\n%s", err, msg)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../testdata/cogs-matrix-14.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("testdata/cogs-matrix-14.json differs from the reference output; regenerate it with python3 -I testdata/gen_cogs_matrix14.py testdata/cogs-matrix-14.json")
	}
}
