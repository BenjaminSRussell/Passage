package domain

import (
	"sort"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/receivables/internal/fixtures"
)

// cashAppPayment is one cash-app-500 payment as the matcher sees it, with the
// pool it is matched against (its legal entity and currency, and its customer
// when the payer is known).
type cashAppPayment struct {
	key, legalEntity, currency, customer string
	pay                                  Payment
}

// loadCashAppPayments reads the 500 payments through the production parsers:
// the lockbox file with ParseBAI2, the bank statement with ParseCAMT053, and
// the remittances as the manual payments they are.
func loadCashAppPayments(t *testing.T, fx fixtures.CashApp) []cashAppPayment {
	t.Helper()
	var out []cashAppPayment
	file, err := ParseBAI2(fx.Lockbox)
	if err != nil {
		t.Fatalf("lockbox: %v", err)
	}
	received := day(fixtures.CashAppReceivedDate)
	for _, c := range file.Checks(DefaultLockboxTypeCodes) {
		p := Payment{Amount: c.Amount, ValueDate: received}
		if c.CustomerRef != "" {
			p.Remittance = []RemittanceLine{{Reference: c.CustomerRef, Amount: c.Amount}}
		}
		out = append(out, cashAppPayment{key: c.BankRef, legalEntity: fixtures.LegalEntityUS, currency: c.Currency, pay: p})
	}
	stmt, err := ParseCAMT053(fx.Statement)
	if err != nil {
		t.Fatalf("statement: %v", err)
	}
	for _, c := range stmt.Credits {
		p := Payment{Amount: c.Amount, ValueDate: day(c.ValueDate), PayerName: c.DebtorName}
		if c.RemittanceRef != "" {
			p.Remittance = []RemittanceLine{{Reference: c.RemittanceRef, Amount: c.Amount}}
		}
		out = append(out, cashAppPayment{key: c.Reference, legalEntity: fixtures.LegalEntityDE, currency: c.Currency, pay: p})
	}
	for _, r := range fx.Remittances {
		p := Payment{Amount: r.Amount, ValueDate: day(r.ValueDate), ShortPayReason: r.ShortPayReason}
		for _, l := range r.Lines {
			p.Remittance = append(p.Remittance, RemittanceLine{Reference: l.Reference, Amount: l.Amount})
		}
		out = append(out, cashAppPayment{key: r.Key, legalEntity: r.LegalEntityID, currency: r.Currency, customer: r.CustomerID, pay: p})
	}
	return out
}

func sameDocuments(got, want []string) bool {
	a := append([]string(nil), got...)
	b := append([]string(nil), want...)
	sort.Strings(a)
	sort.Strings(b)
	return strings.Join(a, " ") == strings.Join(b, " ")
}

// AC-30.1: given cash-app-500, when matching runs, then at least 450 payments
// auto-apply and none applies to items other than the answer key's. This runs
// the pure matcher with no learned payer bindings; the store test runs the same
// fixture through the importers, the database and apply.
func TestCashApp500_AutoMatchesAtLeast450WithNoFalseMatch(t *testing.T) {
	fx, err := fixtures.LoadCashApp()
	if err != nil {
		t.Fatal(err)
	}
	payments := loadCashAppPayments(t, fx)
	if len(payments) != 500 {
		t.Fatalf("payments = %d, want 500", len(payments))
	}
	var pool []Item
	for _, it := range fx.OpenItems {
		item := Item{ID: it.DocumentNumber, CustomerID: it.CustomerID, Currency: it.Currency, DocumentNumber: it.DocumentNumber,
			DueDate: day(it.DueDate), OpenAmount: it.OpenAmount, DiscountAmount: it.DiscountAmount}
		if it.DiscountDate != "" {
			d := day(it.DiscountDate)
			item.DiscountDate = &d
		}
		pool = append(pool, item)
	}
	entity := map[string]string{}
	for _, it := range fx.OpenItems {
		entity[it.DocumentNumber] = it.LegalEntityID
	}

	type tally struct{ total, auto, wrong int }
	byCategory := map[string]*tally{}
	correct, wrong := 0, 0
	for _, p := range payments {
		answer, ok := fx.Key[p.key]
		if !ok {
			t.Fatalf("payment %s is not in the answer key", p.key)
		}
		var candidates []Item
		for _, it := range pool {
			if entity[it.ID] == p.legalEntity && it.Currency == p.currency && (p.customer == "" || it.CustomerID == p.customer) {
				candidates = append(candidates, it)
			}
		}
		res := Match(p.pay, candidates)
		tl := byCategory[answer.Category]
		if tl == nil {
			tl = &tally{}
			byCategory[answer.Category] = tl
		}
		tl.total++
		if res.Decision != DecisionAutoApply {
			continue
		}
		if sameDocuments(res.Top[0].ItemIDs, answer.Documents) {
			correct++
			tl.auto++
		} else {
			wrong++
			tl.wrong++
			t.Errorf("FALSE MATCH %s (%s): auto-applied to %v, answer key %v", p.key, answer.Category, res.Top[0].ItemIDs, answer.Documents)
		}
	}
	for cat, tl := range byCategory {
		t.Logf("%-18s %3d payments, %3d auto-applied correctly, %d wrongly", cat, tl.total, tl.auto, tl.wrong)
	}
	t.Logf("auto-applied correctly: %d of %d (%.1f%%), false matches: %d", correct, len(payments), float64(correct)*100/float64(len(payments)), wrong)
	if correct < 450 {
		t.Errorf("auto-applied correctly = %d, AC-30.1 needs at least 450", correct)
	}
	if wrong != 0 {
		t.Errorf("false matches = %d, AC-30.1 allows none", wrong)
	}
	// Hand-derived from the BR-30.1 weights with no learned payer: a short
	// payment without a reason scores at most R1 60 + R6 5, a payment without a
	// reference R2 30 + R6 5, and a payment quoting the wrong invoice R1 60 + R6 5
	// on the quoted one; all are under the 85 auto-apply threshold.
	for _, cat := range []string{fixtures.CatShortNoReason, fixtures.CatUnidentified, fixtures.CatWrongReference} {
		if tl := byCategory[cat]; tl == nil || tl.total == 0 || tl.auto+tl.wrong != 0 {
			t.Errorf("%s: %+v, want payments and none auto-applied", cat, tl)
		}
	}
}

// AC-30.3: given dunning-60 (60 customers, 5 blocked, 4 disputed, 3 with
// promises), the run proposes letters for exactly the 31 expected customers at
// the expected levels, level 3 fees are 25.00, and no disputed item is on a
// letter. Expected letters and exclusions are hand-derived in testdata/dunning-60.
func TestDunning60_LettersForExactlyTheExpectedCustomers(t *testing.T) {
	fx, err := fixtures.LoadDunning()
	if err != nil {
		t.Fatal(err)
	}
	customers := map[string]bool{}
	disputed := map[string]bool{}
	var items []DunnableItem
	for _, it := range fx.Items {
		customers[it.Customer] = true
		if it.DisputedAmount.IsPositive() {
			disputed[it.OpenItem.DocumentNumber] = true
		}
		if it.OpenItem.OpenAmount.IsZero() {
			continue // settled: the store reads open items only
		}
		di := DunnableItem{ID: it.OpenItem.DocumentNumber, CustomerID: it.Customer, SourceType: "INVOICE",
			DocumentNumber: it.OpenItem.DocumentNumber, Currency: it.OpenItem.Currency, DueDate: day(it.OpenItem.DueDate),
			OpenAmount: it.OpenItem.OpenAmount, DunningLevel: it.DunningLevel, DunningBlock: it.Blocked,
			HasDispute: it.DisputedAmount.IsPositive()}
		if it.PromiseDate != "" {
			d := day(it.PromiseDate)
			di.PromiseDate = &d
		}
		if it.LastDunned != "" {
			d := day(it.LastDunned)
			di.LastDunnedAt = &d
		}
		items = append(items, di)
	}
	if len(customers) != 60 || len(fx.Letters) != 31 {
		t.Fatalf("fixture has %d customers and %d expected letters, want 60 and 31", len(customers), len(fx.Letters))
	}

	plan := PlanDunning(day(fixtures.DunningRunDate), DefaultGraceDays, DefaultIntervalDays, items)
	checkDunning60(t, fx, disputed, toLetters(plan.Letters), plan.Exclusions)
}

// dunningLetterView is the part of a letter dunning-60 checks.
type dunningLetterView struct {
	customer, currency string
	level              int
	fee, amount        decimal.Decimal
	documents          []string
}

func toLetters(in []PlannedLetter) []dunningLetterView {
	out := make([]dunningLetterView, 0, len(in))
	for _, l := range in {
		v := dunningLetterView{customer: l.CustomerID, currency: l.Currency, level: l.Level, fee: l.Fee, amount: l.Amount}
		for _, ln := range l.Lines {
			v.documents = append(v.documents, ln.DocumentNumber)
		}
		out = append(out, v)
	}
	return out
}

func checkDunning60(t *testing.T, fx fixtures.Dunning, disputed map[string]bool, letters []dunningLetterView, exclusions []Exclusion) {
	t.Helper()
	if len(letters) != len(fx.Letters) {
		t.Errorf("letters = %d, want %d", len(letters), len(fx.Letters))
	}
	seen := map[string]bool{}
	for _, l := range letters {
		seen[l.customer] = true
		want, ok := fx.Letters[l.customer]
		if !ok {
			t.Errorf("unexpected letter for %s: %+v", l.customer, l)
			continue
		}
		if l.currency != want.Currency || l.level != want.Level || !l.fee.Equal(want.Fee) || !l.amount.Equal(want.Amount) ||
			!sameDocuments(l.documents, want.Documents) {
			t.Errorf("letter %s = %s L%d fee %s amount %s %v, want %s L%d fee %s amount %s %v", l.customer, l.currency, l.level,
				l.fee, l.amount, l.documents, want.Currency, want.Level, want.Fee, want.Amount, want.Documents)
		}
		if l.level == 3 && l.currency == "USD" && !l.fee.Equal(decimal.NewFromInt(25)) {
			t.Errorf("level 3 letter %s fee = %s, want 25.00", l.customer, l.fee)
		}
		for _, d := range l.documents {
			if disputed[d] {
				t.Errorf("disputed item %s is on %s's letter", d, l.customer)
			}
		}
	}
	for c := range fx.Letters {
		if !seen[c] {
			t.Errorf("no letter for %s", c)
		}
	}
	got := map[string]string{}
	for _, e := range exclusions {
		got[e.OpenItemID] = e.Reason
	}
	if len(got) != len(fx.Exclusions) {
		t.Errorf("exclusions = %d %v, want %d", len(got), got, len(fx.Exclusions))
	}
	for doc, reason := range fx.Exclusions {
		if got[doc] != reason {
			t.Errorf("exclusion of %s = %q, want %q", doc, got[doc], reason)
		}
	}
}
