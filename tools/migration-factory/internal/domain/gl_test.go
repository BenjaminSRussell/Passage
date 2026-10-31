package domain

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

var glAccounts = map[string]struct{}{"1000": {}, "1100": {}, "2100": {}, "3000": {}}

func gl(id, account, dims, cur, amt string) GLBalance {
	return GLBalance{LegacyID: id, Account: account, Dimensions: dims, Currency: cur, Balance: decimal.RequireFromString(amt)}
}

func validatedGLRun() *PackRun {
	return &PackRun{RunID: "r", Pack: PackGLBalances, SourceCompany: "USMF", Status: StatusValidated}
}

// held maps each balance to itself, as if this run had loaded every one.
func heldAll(in []GLBalance) map[string]GLBalance {
	out := make(map[string]GLBalance, len(in))
	for _, g := range in {
		out[g.LegacyID] = g
	}
	return out
}

func totalNamed(t *testing.T, totals []ControlTotal, name string, key map[string]string) ControlTotal {
	t.Helper()
	for _, c := range totals {
		if c.Name == name && sameKey(c.Key, key) {
			return c
		}
	}
	t.Fatalf("no total %s %v in %+v", name, key, totals)
	return ControlTotal{}
}

func TestValidateGL_RulesQuarantineWithReason(t *testing.T) {
	cases := []struct {
		name     string
		in       GLBalance
		wantRule string
	}{
		{"blank legacy id", gl(" ", "1000", "", "USD", "1"), RuleUniqueness},
		{"account not in the chart", gl("GL-1", "9999", "", "USD", "1"), RuleReferential},
		{"blank account", gl("GL-1", "", "", "USD", "1"), RuleReferential},
		{"currency not ISO 4217", gl("GL-1", "1000", "", "XXX", "1"), RuleDomain},
		{"clean balance", gl("GL-1", "1000", "USMF-100", "USD", "1"), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			acc, q := ValidateGL([]GLBalance{c.in}, glAccounts)
			if c.wantRule == "" {
				if len(acc) != 1 || len(q) != 0 {
					t.Fatalf("want accepted, got accepted=%d quarantined=%+v", len(acc), q)
				}
				return
			}
			if len(acc) != 0 || len(q) != 1 || q[0].Rule != c.wantRule || q[0].Reason == "" {
				t.Fatalf("want quarantine by %s with a reason, got %+v", c.wantRule, q)
			}
			if q[0].Record.Dimensions != c.in.Dimensions || !q[0].Record.Amount.Equal(c.in.Balance) {
				t.Fatalf("quarantine must keep the balance as extracted: %+v", q[0].Record)
			}
		})
	}
}

func TestValidateGL_DuplicateIDAndDuplicateNaturalKeyAreUniqueness(t *testing.T) {
	in := []GLBalance{
		gl("GL-1", "1000", "USMF-100", "USD", "1"),
		gl("GL-1", "2100", "", "USD", "-1"),        // same legacy id as the first row
		gl("GL-2", "1000", "USMF-100", "USD", "2"), // same natural key as the first row, new id
		gl("GL-3", "1000", "USMF-100", "EUR", "2"), // same account and dimensions, other currency
	}
	acc, q := ValidateGL(in, glAccounts)
	if len(acc) != 2 || acc[0].LegacyID != "GL-1" || acc[1].LegacyID != "GL-3" || len(q) != 2 {
		t.Fatalf("accepted %+v, quarantined %+v", acc, q)
	}
	for _, qq := range q {
		if qq.Rule != RuleUniqueness {
			t.Fatalf("a duplicate must be UNIQUENESS: %+v", qq)
		}
	}
}

func TestExtractBalanced(t *testing.T) {
	cases := []struct {
		name string
		in   []GLBalance
		want string
	}{
		{"balanced", []GLBalance{gl("a", "1000", "", "USD", "10.00"), gl("b", "2100", "", "USD", "-10.00")}, ""},
		{"off by a cent", []GLBalance{gl("a", "1000", "", "USD", "10.00"), gl("b", "2100", "", "USD", "-9.99")}, "not balanced in USD: debit 10.00, credit 9.99"},
		{"each currency balances on its own", []GLBalance{gl("a", "1000", "", "USD", "5"), gl("b", "2100", "", "EUR", "-5")}, "not balanced in EUR"},
		{"zero balances only", []GLBalance{gl("a", "1000", "", "USD", "0")}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ExtractBalanced(c.in)
			if c.want == "" {
				if err != nil {
					t.Fatalf("want balanced, got %v", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want ErrInvalid naming %q, got %v", c.want, err)
			}
		})
	}
}

func TestReconcileGL_CleanLoadReconciles(t *testing.T) {
	src := []GLBalance{gl("a", "1000", "USMF-100", "USD", "500"), gl("b", "1000", "USMF-200", "USD", "250"), gl("c", "2100", "", "USD", "-750"), gl("z", "3000", "", "USD", "0")}
	r := validatedGLRun()
	if err := ReconcileGL(r, src, src, 4, heldAll(src), nil); err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusReconciled || r.RecordsLoaded != 4 || r.AlreadyPresent != 0 {
		t.Fatalf("run %+v", r)
	}
	// 4 balances, the non-zero count and one trial balance.
	if len(r.Totals) != 6 {
		t.Fatalf("totals %+v", r.Totals)
	}
	if c := totalNamed(t, r.Totals, "gl_balances.nonzero_count", map[string]string{"company": "USMF"}); !c.Target.Equal(decimal.NewFromInt(3)) || c.Status != StatusMatch {
		t.Fatalf("non-zero combinations %+v, want 3", c)
	}
	if c := totalNamed(t, r.Totals, "gl_balances.balance", map[string]string{"company": "USMF", "account": "1000", "dimensions": "USMF-200", "currency": "USD"}); !c.Source.Equal(decimal.NewFromInt(250)) || c.Status != StatusMatch {
		t.Fatalf("balance per dimension combination %+v", c)
	}
}

func TestReconcileGL_HeldBackBalanceBreaksItsTotalAndTheTrialBalance(t *testing.T) {
	src := []GLBalance{gl("a", "1000", "", "USD", "500"), gl("b", "2100", "", "USD", "-300"), gl("q", "9999", "", "USD", "-200")}
	acc, q := ValidateGL(src, glAccounts)
	if len(q) != 1 {
		t.Fatalf("want 9999 quarantined, got %+v", q)
	}
	r := validatedGLRun()
	if err := ReconcileGL(r, src, acc, 2, heldAll(acc), nil); err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusLoaded {
		t.Fatalf("a quarantined balance holds the run at LOADED, got %s", r.Status)
	}
	if c := totalNamed(t, r.Totals, "gl_balances.trial_balance", map[string]string{"company": "USMF", "currency": "USD"}); c.Status != StatusMismatch || !c.Delta.Equal(decimal.NewFromInt(200)) {
		t.Fatalf("loaded trial balance must be off by the held-back credit: %+v", c)
	}
	if c := totalNamed(t, r.Totals, "gl_balances.balance", map[string]string{"company": "USMF", "account": "9999", "dimensions": "", "currency": "USD"}); c.Status != StatusMismatch || !c.Target.IsZero() {
		t.Fatalf("the held-back balance shows as its own mismatch: %+v", c)
	}
}

func TestReconcileGL_RerunComparesWithWhatIsHeld(t *testing.T) {
	src := []GLBalance{gl("a", "1000", "", "USD", "500"), gl("b", "2100", "", "USD", "-500")}
	held := heldAll(src)
	r := validatedGLRun()
	if err := ReconcileGL(r, src, src, 0, held, nil); err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusReconciled || r.RecordsLoaded != 0 || r.AlreadyPresent != 2 || r.Message() == "" {
		t.Fatalf("AC-49.4 re-run loads nothing and stays matched: %+v", r)
	}
	// A re-extract that moved 10.00 between the two balances under the same legacy ids: the held
	// balances are not replaced, so both totals show the difference.
	moved := []GLBalance{gl("a", "1000", "", "USD", "510"), gl("b", "2100", "", "USD", "-510")}
	r = validatedGLRun()
	if err := ReconcileGL(r, moved, moved, 0, held, nil); err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusLoaded {
		t.Fatalf("changed amounts under held legacy ids must not reconcile: %+v", r.Totals)
	}
}

func TestReconcileGL_ControlAccountsAgainstOpenItems(t *testing.T) {
	src := []GLBalance{
		gl("a", "1100", "USMF-100", "USD", "800"), // receivables control account, two combinations
		gl("b", "1100", "USMF-200", "USD", "450"),
		gl("c", "2100", "", "USD", "-1250"), // payables control account
		gl("d", "3000", "", "USD", "0"),
	}
	openItems := []ControlTotal{
		{Name: "open_items.amount_by_account", Key: map[string]string{"company": "USMF", "account": "1100", "currency": "USD"}, Target: decimal.RequireFromString("1250.00")},
		{Name: "open_items.amount_by_account", Key: map[string]string{"company": "USMF", "account": "2100", "currency": "USD"}, Target: decimal.RequireFromString("-1250.01")},
		{Name: "open_items.amount", Key: map[string]string{"company": "USMF", "currency": "USD"}, Target: decimal.RequireFromString("-0.01")},
	}
	r := validatedGLRun()
	if err := ReconcileGL(r, src, src, 4, heldAll(src), openItems); err != nil {
		t.Fatal(err)
	}
	ar := totalNamed(t, r.Totals, ControlAccountName, map[string]string{"company": "USMF", "account": "1100", "currency": "USD"})
	if ar.Status != StatusMatch || !ar.Source.Equal(decimal.NewFromInt(1250)) {
		t.Fatalf("receivables control account sums every dimension combination: %+v", ar)
	}
	ap := totalNamed(t, r.Totals, ControlAccountName, map[string]string{"company": "USMF", "account": "2100", "currency": "USD"})
	if ap.Status != StatusMismatch || !ap.Delta.Equal(decimal.RequireFromString("-0.01")) {
		t.Fatalf("a cent between open items and the GL is a MISMATCH (BR-49.2): %+v", ap)
	}
	if r.Status != StatusLoaded {
		t.Fatalf("BR-49.2: the run must not reach RECONCILED, got %s", r.Status)
	}
	msg := ControlAccountMessage(ap)
	if !strings.HasPrefix(msg, "MIG-1506") || !strings.Contains(msg, "2100 (-1250.01 USD)") || !strings.Contains(msg, "(-1250.00 USD)") {
		t.Fatalf("MIG-1506 text %q", msg)
	}
	for _, c := range r.Totals {
		if c.Name == ControlAccountName && c.Key["account"] == "3000" {
			t.Fatal("an account with no open items is not a control account")
		}
	}
}

func TestReconcileGL_ControlAccountWithNoGLBalanceMismatches(t *testing.T) {
	src := []GLBalance{gl("a", "1000", "", "USD", "1"), gl("b", "3000", "", "USD", "-1")}
	openItems := []ControlTotal{{Name: "open_items.amount_by_account", Key: map[string]string{"company": "USMF", "account": "2100", "currency": "USD"}, Target: decimal.NewFromInt(-40)}}
	r := validatedGLRun()
	if err := ReconcileGL(r, src, src, 2, heldAll(src), openItems); err != nil {
		t.Fatal(err)
	}
	c := totalNamed(t, r.Totals, ControlAccountName, map[string]string{"company": "USMF", "account": "2100", "currency": "USD"})
	if c.Status != StatusMismatch || !c.Source.IsZero() {
		t.Fatalf("open items on an account the GL extract lacks must mismatch: %+v", c)
	}
}

func TestReconcileGL_NeedsAValidatedRun(t *testing.T) {
	r := validatedGLRun()
	r.Status = StatusLoaded
	if err := ReconcileGL(r, nil, nil, 0, nil, nil); !errors.Is(err, ErrTransition) {
		t.Fatalf("want ErrTransition, got %v", err)
	}
}

func TestHashGL_ChangesWithAccountAndIgnoresOrder(t *testing.T) {
	a, b := gl("a", "1000", "X", "USD", "1"), gl("b", "2100", "", "USD", "-1")
	if HashGL([]GLBalance{a, b}) != HashGL([]GLBalance{b, a}) {
		t.Fatal("order must not change the hash")
	}
	moved := a
	moved.Account = "1100"
	if HashGL([]GLBalance{a, b}) == HashGL([]GLBalance{moved, b}) {
		t.Fatal("a balance moved to another account must change the hash")
	}
}

func TestGLProperties(t *testing.T) {
	// An extract built from balanced pairs balances, validates clean when every account is in the
	// chart, and reconciles when everything is loaded; leaving any one non-zero balance out never does.
	rapid.Check(t, func(t *rapid.T) {
		pairs := rapid.SliceOfN(rapid.Int64Range(-100_000_000, 100_000_000), 1, 30).Draw(t, "pairs")
		var in []GLBalance
		for i, p := range pairs {
			amt := decimal.New(p, -2)
			in = append(in,
				GLBalance{LegacyID: fmt.Sprintf("D-%d", i), Account: "1000", Dimensions: fmt.Sprintf("D%d", i), Currency: "USD", Balance: amt},
				GLBalance{LegacyID: fmt.Sprintf("C-%d", i), Account: "2100", Dimensions: fmt.Sprintf("C%d", i), Currency: "USD", Balance: amt.Neg()})
		}
		if err := ExtractBalanced(in); err != nil {
			t.Fatalf("paired extract must balance: %v", err)
		}
		acc, q := ValidateGL(in, glAccounts)
		if len(acc) != len(in) || len(q) != 0 {
			t.Fatalf("clean extract: accepted %d of %d, quarantined %d", len(acc), len(in), len(q))
		}
		r := validatedGLRun()
		if err := ReconcileGL(r, in, acc, len(acc), heldAll(acc), nil); err != nil {
			t.Fatal(err)
		}
		if r.Status != StatusReconciled {
			t.Fatalf("loading every accepted balance must reconcile: %+v", r.Totals)
		}
		drop := rapid.IntRange(0, len(in)-1).Draw(t, "drop")
		if in[drop].Balance.IsZero() {
			return
		}
		held := heldAll(acc)
		delete(held, in[drop].LegacyID)
		r = validatedGLRun()
		if err := ReconcileGL(r, in, acc, len(acc)-1, held, nil); err != nil {
			t.Fatal(err)
		}
		if r.Status == StatusReconciled {
			t.Fatalf("a missing balance %s must not reconcile", in[drop].LegacyID)
		}
	})
	// Validation partitions the extract: nothing is lost or counted twice.
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 30).Draw(t, "n")
		var in []GLBalance
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("GL-%d", rapid.IntRange(0, 20).Draw(t, "id"))
			acct := rapid.SampledFrom([]string{"1000", "2100", "9999"}).Draw(t, "acct")
			cur := rapid.SampledFrom([]string{"USD", "EUR", "ZZZ"}).Draw(t, "cur")
			in = append(in, GLBalance{LegacyID: id, Account: acct, Currency: cur, Balance: decimal.NewFromInt(1)})
		}
		acc, q := ValidateGL(in, glAccounts)
		if len(acc)+len(q) != len(in) {
			t.Fatalf("partition broken: %d accepted + %d quarantined != %d", len(acc), len(q), len(in))
		}
		keys := map[string]bool{}
		for _, g := range acc {
			if keys[g.Key()] {
				t.Fatalf("natural key %s accepted twice", g.Key())
			}
			keys[g.Key()] = true
		}
	})
}
