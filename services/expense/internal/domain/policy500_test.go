package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// testdata/expense-policy-500.json comes from testdata/gen_expense_policy_500.py,
// an independent Python reference of the BR-43.5 / FR-43.12 / FR-43.16 rule
// semantics (python3 -I); it never calls this package.

type fixtureRule struct {
	ID            string   `json:"id"`
	Version       int      `json:"version"`
	EffectiveFrom string   `json:"effectiveFrom"`
	Categories    []string `json:"categories"`
	Basis         string   `json:"basis"`
	Predicate     string   `json:"predicate"`
	Amount        string   `json:"amount"`
	Days          int      `json:"days"`
	NoReceipt     bool     `json:"noReceipt"`
	NoAttendees   bool     `json:"noAttendees"`
	Outcome       string   `json:"outcome"`
	Message       string   `json:"message"`
}

type fixtureReport struct {
	ID      string `json:"id"`
	RuleSet string `json:"ruleSet"`
	Context struct {
		SubmitDate            string   `json:"submitDate"`
		RequisitionCategories []string `json:"requisitionCategories"`
		HasRequisition        bool     `json:"hasRequisition"`
	} `json:"context"`
	Lines []struct {
		ID            string `json:"id"`
		Category      string `json:"category"`
		TxnDate       string `json:"txnDate"`
		Amount        string `json:"amount"`
		Merchant      string `json:"merchant"`
		Attendees     int    `json:"attendees"`
		HasReceipt    bool   `json:"hasReceipt"`
		Justification string `json:"justification"`
		PaidBy        string `json:"paidBy"`
	} `json:"lines"`
	Expected struct {
		Results     []Result `json:"results"`
		Submittable bool     `json:"submittable"`
	} `json:"expected"`
}

func fixtureDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (fr fixtureRule) rule(t *testing.T) Rule {
	basis := Basis(fr.Basis)
	if basis == BasisPerLine {
		basis = "" // the shipped rules leave PER_LINE as the zero value
	}
	return Rule{ID: fr.ID, Version: fr.Version, EffectiveFrom: fixtureDate(t, fr.EffectiveFrom), Categories: fr.Categories,
		Basis: basis, Predicate: Predicate(fr.Predicate), Amount: amt(fr.Amount), Days: fr.Days, NoReceipt: fr.NoReceipt,
		NoAttendees: fr.NoAttendees, Outcome: Outcome(fr.Outcome), Message: fr.Message}
}

func ruleKey(r Rule) string {
	return fmt.Sprintf("%s|%d|%s|%v|%s|%s|%s|%d|%v|%v|%s|%s", r.ID, r.Version, r.EffectiveFrom.Format(time.DateOnly),
		r.Categories, r.Basis, r.Predicate, r.Amount.String(), r.Days, r.NoReceipt, r.NoAttendees, r.Outcome, r.Message)
}

// TestExpensePolicy500_OutcomesMatchExactly is AC3 (first half) / AC-43.6: 500
// reports (2,260 lines: mixed-case categories, 25.00 / 75.00 / 100.00 / 150.00 /
// 400.00 boundaries, PER_DAY meal sums with and without attendees, submissions
// 58 to 90 days late, exact and near duplicates, blank justifications, the
// requisition gate with and without an approved requisition, and a versioned
// rule set whose meal cap changes by transaction date) evaluated under their
// rule set must give exactly the expected results: no false flags, no misses,
// same order, codes, versions and satisfied flags.
func TestExpensePolicy500_OutcomesMatchExactly(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "expense-policy-500.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		RuleSets map[string][]fixtureRule `json:"ruleSets"`
		Reports  []fixtureReport          `json:"reports"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Reports) != 500 {
		t.Fatalf("fixture has %d reports, want 500", len(doc.Reports))
	}

	// The fixture's DEFAULT set must be the shipped rule set, which is what
	// the reports are evaluated under.
	shipped := DefaultRules()
	if len(doc.RuleSets["DEFAULT"]) != len(shipped) {
		t.Fatalf("fixture DEFAULT has %d rules, DefaultRules %d", len(doc.RuleSets["DEFAULT"]), len(shipped))
	}
	for i, fr := range doc.RuleSets["DEFAULT"] {
		if got, want := ruleKey(fr.rule(t)), ruleKey(shipped[i]); got != want {
			t.Fatalf("fixture DEFAULT rule %d = %s, DefaultRules has %s", i, got, want)
		}
	}
	sets := map[string][]Rule{"DEFAULT": shipped}
	for name, frs := range doc.RuleSets {
		if name == "DEFAULT" {
			continue
		}
		for _, fr := range frs {
			sets[name] = append(sets[name], fr.rule(t))
		}
	}

	exact, falseFlags, misses, lines := 0, 0, 0, 0
	for _, rep := range doc.Reports {
		ls := make([]Line, 0, len(rep.Lines))
		for _, l := range rep.Lines {
			ls = append(ls, Line{ID: l.ID, Category: l.Category, TxnDate: fixtureDate(t, l.TxnDate), Amount: amt(l.Amount),
				Merchant: l.Merchant, Attendees: l.Attendees, HasReceipt: l.HasReceipt, Justification: l.Justification, PaidBy: l.PaidBy})
		}
		lines += len(ls)
		rules, ok := sets[rep.RuleSet]
		if !ok {
			t.Fatalf("%s: unknown rule set %q", rep.ID, rep.RuleSet)
		}
		got := EvaluateReport(rules, ls, Context{SubmitDate: fixtureDate(t, rep.Context.SubmitDate),
			RequisitionCategories: rep.Context.RequisitionCategories, HasRequisition: rep.Context.HasRequisition})

		want := rep.Expected.Results
		count := func(rs []Result) map[Result]int {
			m := map[Result]int{}
			for _, r := range rs {
				m[r]++
			}
			return m
		}
		gc, wc := count(got), count(want)
		for r, n := range gc {
			if extra := n - wc[r]; extra > 0 {
				falseFlags += extra
				t.Errorf("%s: false flag %+v", rep.ID, r)
			}
		}
		for r, n := range wc {
			if missing := n - gc[r]; missing > 0 {
				misses += missing
				t.Errorf("%s: missed %+v", rep.ID, r)
			}
		}
		same := len(got) == len(want)
		for i := 0; same && i < len(got); i++ {
			same = got[i] == want[i]
		}
		if !same {
			t.Errorf("%s: results %+v, want %+v", rep.ID, got, want)
		}
		if Submittable(got) != rep.Expected.Submittable {
			t.Errorf("%s: submittable %v, want %v", rep.ID, Submittable(got), rep.Expected.Submittable)
			same = false
		}
		if same {
			exact++
		}
	}
	t.Logf("expense-policy-500: %d/500 reports exact over %d lines; %d false flags, %d misses", exact, lines, falseFlags, misses)
}

// TestExpensePolicy500FixtureMatchesReference regenerates the fixture with the
// Python reference and requires the committed file to be identical.
func TestExpensePolicy500FixtureMatchesReference(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	dir := t.TempDir()
	out, err := exec.Command(py, "-I", filepath.Join("testdata", "gen_expense_policy_500.py"), dir).CombinedOutput()
	if err != nil {
		t.Fatalf("reference failed: %v\n%s", err, out)
	}
	want, err := os.ReadFile(filepath.Join(dir, "expense-policy-500.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join("testdata", "expense-policy-500.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("testdata/expense-policy-500.json differs from the reference output")
	}
}
