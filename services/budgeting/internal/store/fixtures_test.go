package store

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/domain"
	"github.com/BenjaminSRussell/passage/services/budgeting/migrations"
)

// checkFixtures is the part of testdata/budget-check-fixtures.json the store
// tests replay end to end. Expected values are the file's hand-derived literals.
type checkFixtures struct {
	Policies map[string]struct {
		WarnPct  string `json:"warnPct"`
		BlockPct string `json:"blockPct"`
	} `json:"policies"`
	Balances map[string]struct {
		Budget          string `json:"budget"`
		Actuals         string `json:"actuals"`
		Encumbrances    string `json:"encumbrances"`
		PreEncumbrances string `json:"preEncumbrances"`
		Reservations    string `json:"reservations"`
		Available       string `json:"available"`
	} `json:"balances"`
	Checks []struct {
		ID       string `json:"id"`
		Policy   string `json:"policy"`
		Balance  string `json:"balance"`
		Amount   string `json:"amount"`
		Prior    string `json:"prior"`
		Relieved string `json:"relieved"`
		Override bool   `json:"override"`
		Want     struct {
			Result  domain.Result `json:"result"`
			Net     string        `json:"net"`
			OverPct string        `json:"overPct"`
			Reserve string        `json:"reserve"`
		} `json:"want"`
	} `json:"checks"`
	Liquidations []struct {
		ID      string `json:"id"`
		Reserve string `json:"reserve"`
		Steps   []struct {
			Op           string `json:"op"`
			Amount       string `json:"amount"`
			MatchedShare string `json:"matchedShare"`
			Reason       string `json:"reason"`
			Want         struct {
				Relief    string `json:"relief"`
				Excess    string `json:"excess"`
				Released  string `json:"released"`
				Remaining string `json:"remaining"`
				Status    string `json:"status"`
			} `json:"want"`
		} `json:"steps"`
	} `json:"liquidations"`
}

func loadCheckFixtures(t *testing.T) checkFixtures {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/budget-check-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var f checkFixtures
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func dz(s string) decimal.Decimal {
	if s == "" {
		return decimal.Zero
	}
	return d(s)
}

// fixtureLedger migrates a schema and creates one rule version per fixture policy.
func fixtureLedger(t *testing.T, f checkFixtures) (context.Context, *Postgres, map[string]string) {
	t.Helper()
	pool := testkit.DB(t)
	ctx := testCtx(testkit.TenantID(38), "budget.configure", "budget.maintain", "budget.check")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(pool)
	le := ids.New()
	rules := map[string]string{}
	for name, p := range f.Policies {
		_, raw, err := st.CreateRule(ctx, ids.New(), "h", NewRule{LegalEntityID: le, Code: "FIX-" + strings.ToUpper(name), WarnPct: d(p.WarnPct), BlockPct: d(p.BlockPct),
			Currency: "USD", EffectiveFrom: "2026-01-01"})
		if err != nil {
			t.Fatal(err)
		}
		var r Rule
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		rules[name] = r.ID
	}
	return ctx, st, rules
}

func postBudget(t *testing.T, ctx context.Context, st *Postgres, ruleID, dim, amount string) {
	t.Helper()
	if _, _, err := st.PostRegisterEntry(ctx, ids.New(), "h", RegisterRequest{RuleVersionID: ruleID, EntryType: domain.EntryOriginal,
		Lines: []domain.RegisterLine{{DimensionKey: dim, SpanKey: "FY2026", Amount: d(amount)}}}); err != nil {
		t.Fatal(err)
	}
}

func balanceOf(t *testing.T, ctx context.Context, st *Postgres, ruleID, dim string) BalanceView {
	t.Helper()
	rows, err := st.Available(ctx, ruleID, dim, "FY2026")
	if err != nil || len(rows) != 1 {
		t.Fatalf("available %s: %v (%d rows)", dim, err, len(rows))
	}
	return rows[0]
}

// Wave 38 proof, AC "POs over budget are blocked/warned per policy", through the
// store: every fixture line is checked under row lock against a stored balance
// and a stored prior reservation. Fail reserves nothing; pass and warn move the
// balance and the line's encumbrance by exactly the hand-derived reservation.
func TestBudgetCheckFixtures_StoreOutcomePerPolicy(t *testing.T) {
	f := loadCheckFixtures(t)
	ctx, st, rules := fixtureLedger(t, f)
	for _, c := range f.Checks {
		t.Run(c.ID, func(t *testing.T) {
			b := f.Balances[c.Balance]
			rule, dim := rules[c.Policy], "fx-"+c.ID
			postBudget(t, ctx, st, rule, dim, b.Budget)
			line := CheckLine{LineID: ids.New(), DimensionKey: dim, SpanKey: "FY2026"}
			doc := ids.New()
			if prior := dz(c.Prior); prior.IsPositive() {
				line.Amount = prior
				_, raw, err := st.CheckBudget(ctx, ids.New(), "h", CheckRequest{RuleVersionID: rule, DocumentID: doc, DocumentType: "PurchaseOrder", Lines: []CheckLine{line}}, false)
				if err != nil || decodeCheck(t, raw).Result != domain.Pass {
					t.Fatalf("seeding the prior reservation: %s (%v)", raw, err)
				}
				if relieved := dz(c.Relieved); relieved.IsPositive() {
					if _, _, err := st.RelieveEncumbrance(ctx, ids.New(), "h", RelieveRequest{EncumbranceID: decodeCheck(t, raw).Lines[0].EncumbranceID,
						InvoiceID: ids.New(), InvoiceAmount: relieved, MatchedShare: relieved}); err != nil {
						t.Fatal(err)
					}
				}
			}
			// Put the combination in the fixture's state; the fixture's encumbrances
			// already include the line's own prior reservation.
			if err := dbx.WithTenantTx(ctx, st.pool, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `update budget_control_balance set actuals = $3, encumbrances = $4, pre_encumbrances = $5, reservations = $6
					where rule_version_id = $1 and control_dimension_key = $2`, rule, dim, dz(b.Actuals), dz(b.Encumbrances), dz(b.PreEncumbrances), dz(b.Reservations))
				return err
			}); err != nil {
				t.Fatal(err)
			}
			line.Amount = d(c.Amount)
			_, raw, err := st.CheckBudget(ctx, ids.New(), "h", CheckRequest{RuleVersionID: rule, DocumentID: doc, DocumentType: "PurchaseOrder", Lines: []CheckLine{line}}, c.Override)
			if err != nil {
				t.Fatal(err)
			}
			resp := decodeCheck(t, raw)
			got := resp.Lines[0]
			if resp.Result != c.Want.Result || got.Result != c.Want.Result {
				t.Fatalf("result = %s / line %s, want %s (trace %v)", resp.Result, got.Result, c.Want.Result, got.Trace)
			}
			if !got.Available.Equal(dz(b.Available)) || !got.Net.Equal(dz(c.Want.Net)) || !got.Prior.Equal(dz(c.Prior)) {
				t.Errorf("available %s net %s prior %s, want %s / %s / %s", got.Available, got.Net, got.Prior, b.Available, c.Want.Net, dz(c.Prior))
			}
			if c.Want.OverPct != "" && !got.OverPct.Equal(dz(c.Want.OverPct)) {
				t.Errorf("overPct = %s, want %s", got.OverPct, c.Want.OverPct)
			}
			if resp.Overridden != (c.Override && c.Want.Result == domain.Warn) {
				t.Errorf("overridden = %v", resp.Overridden)
			}
			wantEnc := dz(b.Encumbrances).Add(dz(c.Want.Reserve))
			if after := balanceOf(t, ctx, st, rule, dim); !after.Encumbrances.Equal(wantEnc) {
				t.Errorf("balance encumbrances after = %s, want %s + %s", after.Encumbrances, b.Encumbrances, c.Want.Reserve)
			}
			wantReserved := dz(c.Prior).Add(dz(c.Want.Reserve))
			if c.Want.Result == domain.Fail {
				wantReserved = dz(c.Prior)
			}
			encs, err := st.ListEncumbrances(ctx, rule, "")
			if err != nil {
				t.Fatal(err)
			}
			reserved := decimal.Zero
			for _, e := range encs {
				if e.SourceLineID == line.LineID {
					reserved = e.Reserved
				}
			}
			if !reserved.Equal(wantReserved) {
				t.Errorf("line reservation = %s, want %s", reserved, wantReserved)
			}
		})
	}
}

// Wave 38 proof, AC "encumbrance liquidation on invoice is exact", through the
// store: each fixture PO line is reserved, invoiced, released and invoiced
// again; every step reports the hand-derived relief, excess and remaining, and
// the balance's encumbrances equal the line's open remainder after each step.
func TestBudgetCheckFixtures_StoreLiquidationIsExact(t *testing.T) {
	f := loadCheckFixtures(t)
	ctx, st, rules := fixtureLedger(t, f)
	rule := rules["dept"]
	for _, l := range f.Liquidations {
		t.Run(l.ID, func(t *testing.T) {
			dim := "lq-" + l.ID
			postBudget(t, ctx, st, rule, dim, "100000")
			req := CheckRequest{RuleVersionID: rule, DocumentID: ids.New(), DocumentType: "PurchaseOrder",
				Lines: []CheckLine{{LineID: ids.New(), DimensionKey: dim, SpanKey: "FY2026", Amount: d(l.Reserve)}}}
			_, raw, err := st.CheckBudget(ctx, ids.New(), "h", req, false)
			if err != nil || decodeCheck(t, raw).Result != domain.Pass {
				t.Fatalf("PO check: %s (%v)", raw, err)
			}
			encID := decodeCheck(t, raw).Lines[0].EncumbranceID
			for i, s := range l.Steps {
				var remaining decimal.Decimal
				var status string
				switch s.Op {
				case "invoice":
					_, raw, err := st.RelieveEncumbrance(ctx, ids.New(), "h", RelieveRequest{EncumbranceID: encID, InvoiceID: ids.New(),
						InvoiceAmount: d(s.Amount), MatchedShare: d(s.MatchedShare)})
					var rel RelieveResponse
					if err == nil {
						err = json.Unmarshal(raw, &rel)
					}
					if err != nil {
						t.Fatalf("step %d: %v", i, err)
					}
					if !rel.Relieved.Equal(d(s.Want.Relief)) || !rel.Excess.Equal(d(s.Want.Excess)) {
						t.Fatalf("step %d: relief %s excess %s, want %s / %s", i, rel.Relieved, rel.Excess, s.Want.Relief, s.Want.Excess)
					}
					remaining, status = rel.Remaining, rel.Status
				case "release":
					_, raw, err := st.ReleaseEncumbrance(ctx, ids.New(), "h", ReleaseRequest{EncumbranceID: encID, DocumentID: req.DocumentID, Reason: s.Reason})
					var rel ReleaseResponse
					if err == nil {
						err = json.Unmarshal(raw, &rel)
					}
					if err != nil {
						t.Fatalf("step %d: %v", i, err)
					}
					if !rel.Released.Equal(d(s.Want.Released)) {
						t.Fatalf("step %d: released %s, want %s", i, rel.Released, s.Want.Released)
					}
					remaining, status = rel.Remaining, rel.Status
				default:
					t.Fatalf("step %d: unknown op %q", i, s.Op)
				}
				if !remaining.Equal(d(s.Want.Remaining)) || status != s.Want.Status {
					t.Fatalf("step %d: remaining %s status %s, want %s / %s", i, remaining, status, s.Want.Remaining, s.Want.Status)
				}
				if bal := balanceOf(t, ctx, st, rule, dim); !bal.Encumbrances.Equal(d(s.Want.Remaining)) {
					t.Fatalf("step %d: balance encumbrances %s, want the open %s", i, bal.Encumbrances, s.Want.Remaining)
				}
			}
		})
	}
	assertEncumbranceInvariant(t, ctx, st, rule)
}
