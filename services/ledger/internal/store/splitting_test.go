package store

import (
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

func splitSetup(t *testing.T) (env, domain.SplittingRule) {
	t.Helper()
	e, _ := setup(t)
	for num, a := range map[string]Account{
		"1540": {Name: "Input tax", Type: "asset", Postable: true},
		"1999": {Name: "Zero-balance clearing", Type: "asset", Postable: true},
		"1990": {Name: "Total account", Type: "balance_sheet_total", Postable: false},
	} {
		if _, _, err := e.store.PutAccount(e.ctx, "STD", num, a); err != nil {
			t.Fatalf("put account %s: %v", num, err)
		}
	}
	rule, created, err := e.store.PutSplittingRule(e.ctx, e.ledger.ID, ids.New(), 0, domain.SplittingRule{SourceType: "vendor_invoice",
		SplittingDimensions: []string{"DEPT"}, ZeroBalanceClearingAccount: "1999", Active: true})
	if err != nil || !created || rule.Version != 1 || rule.ID == "" || !rule.Active {
		t.Fatalf("put rule: %+v created=%v err=%v", rule, created, err)
	}
	return e, rule
}

func splitInvoice(e env, key string) PostInput {
	in := vendorInvoice(e.ledger.ID, key, "600.00")
	d := func(a, side, amt string, dims map[string]string) domain.LineInput {
		return domain.LineInput{Account: a, Side: side, Amount: decimal.RequireFromString(amt), Currency: "USD", Dimensions: dims}
	}
	in.Lines = []domain.LineInput{
		d("6010", "D", "600.00", map[string]string{"DEPT": "D100"}),
		d("6010", "D", "400.00", map[string]string{"DEPT": "D200"}),
		d("1540", "D", "190.00", nil),
		d("2100", "C", "1190.00", nil),
	}
	return in
}

func deptNets(t *testing.T, lines []Line) map[string]string {
	t.Helper()
	sums := map[string]decimal.Decimal{}
	for _, l := range lines {
		a := decimal.RequireFromString(l.Amount)
		if l.DebitCredit == domain.SideCredit {
			a = a.Neg()
		}
		sums[l.Dimensions["DEPT"]] = sums[l.Dimensions["DEPT"]].Add(a)
	}
	out := map[string]string{}
	for k, v := range sums {
		out[k] = v.StringFixed(2)
	}
	return out
}

// AC-11.16: the payable and the tax line inherit the department in proportion to the expense lines.
func TestPostgres_AC11_16_SplittingInheritsProportionally(t *testing.T) {
	e, _ := splitSetup(t)
	in := splitInvoice(e, "SP-1")
	v, err := e.store.ValidateEntry(e.ctx, in)
	if err != nil || !v.Valid() || len(v.Splitting) != 4 {
		t.Fatalf("validate: %+v err=%v", v, err)
	}
	res, err := e.store.PostEntry(e.ctx, in)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	got := map[string]string{}
	lines := res.Entries[0].Lines
	for _, l := range lines {
		if l.Account == "2100" || l.Account == "1540" {
			got[l.Account+l.Dimensions["DEPT"]] = l.Amount
		}
	}
	want := map[string]string{"2100D100": "714.00", "2100D200": "476.00", "1540D100": "114.00", "1540D200": "76.00"}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q (all %v)", k, got[k], w, got)
		}
	}
	for k, n := range deptNets(t, lines) {
		if n != "0.00" {
			t.Errorf("department %q nets %s, want 0.00", k, n)
		}
	}
	if len(lines) != 6 {
		t.Errorf("lines = %d, want 6", len(lines))
	}
	// The idempotent replay returns the split entry, and the projection holds the split balances.
	replay, err := e.store.PostEntry(e.ctx, in)
	if err != nil || !replay.Idempotency.Replayed || len(replay.Entries[0].Lines) != 6 {
		t.Fatalf("replay: %+v err=%v", replay, err)
	}
	// Another source type is not split: the unsplit lines post with blank departments.
	other := splitInvoice(e, "SP-2")
	other.Source.Type = "manual_other"
	r2, err := e.store.PostEntry(e.ctx, other)
	if err != nil || len(r2.Entries[0].Lines) != 4 {
		t.Fatalf("other source type: %+v err=%v", r2, err)
	}
}

func TestPostgres_SplittingClearingLinesAndFailures(t *testing.T) {
	e, rule := splitSetup(t)
	// A transfer between departments leaves each department unbalanced; clearing lines fix it.
	tr := vendorInvoice(e.ledger.ID, "SP-3", "100.00")
	tr.Lines = []domain.LineInput{
		{Account: "6010", Side: "D", Amount: decimal.NewFromInt(100), Currency: "USD", Dimensions: map[string]string{"DEPT": "D100"}},
		{Account: "2100", Side: "C", Amount: decimal.NewFromInt(100), Currency: "USD", Dimensions: map[string]string{"DEPT": "D200"}},
	}
	res, err := e.store.PostEntry(e.ctx, tr)
	if err != nil || len(res.Entries[0].Lines) != 4 {
		t.Fatalf("transfer: %+v err=%v", res, err)
	}
	for k, n := range deptNets(t, res.Entries[0].Lines) {
		if n != "0.00" {
			t.Errorf("department %q nets %s", k, n)
		}
	}
	// No carrying line and no default: LEDGER_SPLIT_NO_SOURCE for every unsplit line.
	none := vendorInvoice(e.ledger.ID, "SP-4", "10.00")
	none.Lines[0].Dimensions = nil
	_, err = e.store.PostEntry(e.ctx, none)
	if rej := rejection(err); rej == nil || rej.Violations[0].Code != domain.CodeSplitNoSource {
		t.Fatalf("no source: %v", err)
	}
	// A default value splits the whole entry to it.
	rule.DefaultSplittingValue = map[string]string{"DEPT": "D900"}
	rule, _, err = e.store.PutSplittingRule(e.ctx, e.ledger.ID, rule.ID, rule.Version, rule)
	if err != nil || rule.Version != 2 || rule.DefaultSplittingValue["DEPT"] != "D900" {
		t.Fatalf("replace: %+v err=%v", rule, err)
	}
	r, err := e.store.PostEntry(e.ctx, none)
	if err != nil || r.Entries[0].Lines[0].Dimensions["DEPT"] != "D900" {
		t.Fatalf("default value: %+v err=%v", r, err)
	}
	// An inactive rule is not applied.
	rule.Active = false
	if _, _, err = e.store.PutSplittingRule(e.ctx, e.ledger.ID, rule.ID, rule.Version, rule); err != nil {
		t.Fatal(err)
	}
	none2 := vendorInvoice(e.ledger.ID, "SP-5", "10.00")
	none2.Lines[0].Dimensions = nil
	if r, err := e.store.PostEntry(e.ctx, none2); err != nil || r.Entries[0].Lines[0].Dimensions["DEPT"] != "" {
		t.Fatalf("inactive rule: %+v err=%v", r, err)
	}
}

func TestPostgres_SplittingClearingAccountRequiredWhenNeeded(t *testing.T) {
	e, _ := setup(t)
	if _, _, err := e.store.PutSplittingRule(e.ctx, e.ledger.ID, ids.New(), 0, domain.SplittingRule{SourceType: "vendor_invoice",
		SplittingDimensions: []string{"DEPT"}, Active: true}); err != nil {
		t.Fatal(err)
	}
	tr := vendorInvoice(e.ledger.ID, "SP-6", "100.00")
	tr.Lines[1].Dimensions = map[string]string{"DEPT": "D200"}
	_, err := e.store.PostEntry(e.ctx, tr)
	if rej := rejection(err); rej == nil || rej.Violations[0].Code != domain.CodeSplitClearingMissing {
		t.Fatalf("missing clearing account: %v", err)
	}
	// A balanced single-department entry needs no clearing line.
	if _, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "SP-7", "100.00")); err != nil {
		t.Fatalf("balanced entry: %v", err)
	}
}

func rejection(err error) *domain.RejectedError {
	var rej *domain.RejectedError
	if errors.As(err, &rej) {
		return rej
	}
	return nil
}

func TestPostgres_SplittingRuleMaintenance(t *testing.T) {
	e, rule := splitSetup(t)
	reject := func(name string, in domain.SplittingRule) {
		t.Helper()
		_, _, err := e.store.PutSplittingRule(e.ctx, e.ledger.ID, ids.New(), 0, in)
		if rej := rejection(err); rej == nil || rej.Violations[0].Code != domain.CodeSplittingInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	reject("unknown dimension", domain.SplittingRule{SourceType: "a", SplittingDimensions: []string{"NOPE"}})
	reject("no dimension", domain.SplittingRule{SourceType: "a"})
	reject("unknown clearing account", domain.SplittingRule{SourceType: "a", SplittingDimensions: []string{"DEPT"}, ZeroBalanceClearingAccount: "0000"})
	reject("total account", domain.SplittingRule{SourceType: "a", SplittingDimensions: []string{"DEPT"}, ZeroBalanceClearingAccount: "1990"})
	reject("expense clearing account", domain.SplittingRule{SourceType: "a", SplittingDimensions: []string{"DEPT"}, ZeroBalanceClearingAccount: "6010"})
	reject("reconciliation clearing account", domain.SplittingRule{SourceType: "a", SplittingDimensions: []string{"DEPT"}, ZeroBalanceClearingAccount: "2110"})
	reject("default outside dimensions", domain.SplittingRule{SourceType: "a", SplittingDimensions: []string{"DEPT"}, DefaultSplittingValue: map[string]string{"CC": "C1"}})

	if _, _, err := e.store.PutSplittingRule(e.ctx, e.ledger.ID, ids.New(), 0, domain.SplittingRule{SourceType: "vendor_invoice",
		SplittingDimensions: []string{"DEPT"}}); problemCode(err) != CodeSplittingDuplicate {
		t.Fatalf("duplicate source type: %v", err)
	}
	if _, _, err := e.store.PutSplittingRule(e.ctx, e.ledger.ID, rule.ID, 7, rule); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	// PUT is idempotent: the same content again keeps the version and announces nothing.
	same, created, err := e.store.PutSplittingRule(e.ctx, e.ledger.ID, rule.ID, rule.Version, rule)
	if err != nil || created || same.Version != rule.Version {
		t.Fatalf("repeated put: %+v created=%v err=%v", same, created, err)
	}
	list, err := e.store.ListSplittingRules(e.ctx, e.ledger.ID)
	if err != nil || len(list) != 1 || list[0].ZeroBalanceClearingAccount != "1999" {
		t.Fatalf("list: %+v %v", list, err)
	}
	if _, err := e.store.GetSplittingRule(e.ctx, e.ledger.ID, ids.New()); problemCode(err) != CodeSplittingNotFound {
		t.Fatalf("missing rule: %v", err)
	}
	// rules.changed with kind splitting_rule went through the outbox; another tenant sees nothing.
	var n int
	err = dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `select count(*) from outbox where type = $1 and aggregate_type = 'splitting_rule'`, ledgerevents.RulesChanged).Scan(&n)
	})
	if err != nil || n != 1 {
		t.Fatalf("outbox rows = %d err=%v", n, err)
	}
	other := principalCtx(testkit.TenantID(2), "")
	if _, err := e.store.ListSplittingRules(other, e.ledger.ID); !problemStatusIs(err, http.StatusNotFound) {
		t.Fatalf("other tenant list: %v", err)
	}
	var rls int
	if err := e.admin.QueryRow(e.ctx, `select count(*) from pg_class where relname = 'splitting_rule' and relrowsecurity and relforcerowsecurity`).Scan(&rls); err != nil || rls != 1 {
		t.Error("splitting_rule lacks forced row-level security")
	}
}

// Splitting runs before the validation rules (PRD posting pipeline): a rule that requires a
// department on the payable sees the department the payable inherited.
func TestPostgres_SplittingRunsBeforeValidationRules(t *testing.T) {
	e, _ := splitSetup(t)
	if _, _, err := e.store.PutValidationRule(e.ctx, e.ledger.ID, ids.New(), 0, domain.ValidationRule{Sequence: 10, Name: "DEPT on payables",
		Level: domain.RuleLevelLine, Severity: domain.SeverityError, Active: true, Message: "Payables need a department",
		Prerequisite: []domain.RuleCondition{{Field: domain.FieldAccount, Op: domain.CondEq, Values: []string{"2100"}}},
		Checks:       []domain.RuleCondition{{Field: "DEPT", Op: domain.CondNotBlank}}}); err != nil {
		t.Fatal(err)
	}
	in := splitInvoice(e, "SP-VR")
	if v, err := e.store.ValidateEntry(e.ctx, in); err != nil || !v.Valid() {
		t.Fatalf("validate: %+v err=%v", v, err)
	}
	if _, err := e.store.PostEntry(e.ctx, in); err != nil {
		t.Fatalf("post: %v", err)
	}
	// Without a splitting rule for the source type the same rule refuses the payable.
	other := splitInvoice(e, "SP-VR2")
	other.Source.Type = "manual_other"
	if _, err := e.store.PostEntry(e.ctx, other); !slices.Contains(rejectionCodes(err), domain.CodeValidationRuleFail) {
		t.Fatalf("unsplit payable: %v", err)
	}
}
