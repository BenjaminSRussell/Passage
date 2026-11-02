package store

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
)

type camtEntry struct{ ref, amount string }

func camtEntries(id string, entries []camtEntry) []byte {
	var b strings.Builder
	closing := decimal.RequireFromString("1000.00")
	for _, e := range entries {
		closing = closing.Add(decimal.RequireFromString(e.amount))
	}
	fmt.Fprintf(&b, `<?xml version="1.0" encoding="UTF-8"?><Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.053.001.08"><BkToCstmrStmt><Stmt><Id>%s</Id>`, id)
	fmt.Fprintf(&b, `<Acct><Id><IBAN>%s</IBAN></Id><Ccy>EUR</Ccy></Acct>`, testIBAN)
	b.WriteString(`<Bal><Tp><CdOrPrtry><Cd>OPBD</Cd></CdOrPrtry></Tp><Amt Ccy="EUR">1000.00</Amt><CdtDbtInd>CRDT</CdtDbtInd><Dt><Dt>2026-10-22</Dt></Dt></Bal>`)
	fmt.Fprintf(&b, `<Bal><Tp><CdOrPrtry><Cd>CLBD</Cd></CdOrPrtry></Tp><Amt Ccy="EUR">%s</Amt><CdtDbtInd>CRDT</CdtDbtInd><Dt><Dt>2026-10-22</Dt></Dt></Bal>`, closing.StringFixed(2))
	for _, e := range entries {
		fmt.Fprintf(&b, `<Ntry><Amt Ccy="EUR">%s</Amt><CdtDbtInd>CRDT</CdtDbtInd><BookgDt><Dt>2026-10-22</Dt></BookgDt><NtryDtls><TxDtls><Refs><EndToEndId>%s</EndToEndId></Refs></TxDtls></NtryDtls></Ntry>`, e.amount, e.ref)
	}
	b.WriteString(`</Stmt></BkToCstmrStmt></Document>`)
	return []byte(b.String())
}

func refRule(le string, account *string, name string, priority int) NewReconRule {
	return NewReconRule{LegalEntityID: le, BankAccountID: account, Rule: domain.Rule{
		Name: name, Priority: priority, Weights: domain.Weights{Amount: 40, Date: 20, Reference: 40},
		Tolerance: domain.Tolerance{Amount: decimal.RequireFromString("0.05"), DateWindowDays: 2},
	}}
}

func runItem(kind, id, amount, reference, batch string) RunItem {
	return RunItem{Type: kind, ID: id, Amount: amount, Currency: "EUR", ValueDate: "2026-10-22", Reference: reference, Batch: batch}
}

type reconFixture struct {
	s         *Postgres
	admin     *pgxpool.Pool
	ctx       context.Context
	le        string
	account   BankAccount
	statement Statement
	items     []RunItem
}

func newReconFixture(t *testing.T) *reconFixture {
	t.Helper()
	s, admin := newStore(t)
	le := ids.New()
	ctx := ctxFor(1, le)
	acct := activeAccount(t, s, ctx, le)
	res, err := s.ImportStatement(ctx, importInput(t, acct.ID, camtEntries("STMT-REC", []camtEntry{
		{"INV-2026-0001", "100.00"}, {"INV-2026-0002", "50.00"}, {"RMT-2026-0001", "300.00"}, {"MISC", "7.00"},
	})))
	if err != nil {
		t.Fatal(err)
	}
	return &reconFixture{s: s, admin: admin, ctx: ctx, le: le, account: acct, statement: res.Statement, items: []RunItem{
		runItem("ar", "inv-1", "100.00", "INV-2026-0001", ""),
		runItem("ar", "inv-2", "50.00", "INV-2026-0002", ""),
		runItem("ar", "b-1", "100.00", "INV-B1", "RMT-2026-0001"),
		runItem("ar", "b-2", "200.00", "INV-B2", "RMT-2026-0001"),
	}}
}

func (f *reconFixture) addRule(t *testing.T) ReconRule {
	t.Helper()
	r, err := f.s.CreateReconRule(f.ctx, refRule(f.le, &f.account.ID, "reference", 1), ids.New())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func lineStatuses(t *testing.T, f *reconFixture) map[int]string {
	t.Helper()
	lines, err := f.s.ListStatementLines(f.ctx, f.statement.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int]string{}
	for _, l := range lines {
		out[l.LineNo] = l.Status
	}
	return out
}

func TestRunReconciliation_NeedsAnActiveRule(t *testing.T) {
	f := newReconFixture(t)
	_, err := f.s.RunReconciliation(f.ctx, RunInput{StatementID: f.statement.ID, Items: f.items}, ids.New())
	if got := problemCode(t, err); got != CodeNoRules {
		t.Fatalf("code = %s; want %s", got, CodeNoRules)
	}
	rule := f.addRule(t)
	if _, err := f.s.DeactivateReconRule(f.ctx, rule.ID, rule.Version); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.RunReconciliation(f.ctx, RunInput{StatementID: f.statement.ID, Items: f.items}, ids.New())
	if got := problemCode(t, err); got != CodeNoRules {
		t.Fatalf("after deactivate: code = %s; want %s", got, CodeNoRules)
	}
}

func TestRunReconciliation_StoresOneToOneAndBatchMatchesOnceAndPublishesEvents(t *testing.T) {
	f := newReconFixture(t)
	admin := f.admin
	f.addRule(t)
	key := ids.New()
	res, err := f.s.RunReconciliation(f.ctx, RunInput{StatementID: f.statement.ID, Items: f.items}, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 3 || res.LinesConsidered != 4 || res.ItemsConsidered != 4 {
		t.Fatalf("result = %+v; want 3 matches over 4 lines and 4 items", res)
	}
	var batch *Match
	for i, m := range res.Matches {
		if m.RuleName == domain.BatchRule {
			batch = &res.Matches[i]
		}
	}
	if batch == nil || len(batch.Items) != 2 || len(batch.Lines) != 1 || batch.Lines[0].LineNo != 3 || batch.Difference != "0" {
		t.Fatalf("batch match = %+v; want line 3 against two items", batch)
	}
	want := map[int]string{1: "matched", 2: "matched", 3: "matched", 4: "unmatched"}
	for no, status := range lineStatuses(t, f) {
		if status != want[no] {
			t.Errorf("line %d status = %s; want %s", no, status, want[no])
		}
	}
	if n := count(t, admin, `select count(*) from outbox where type = 'dev.passage.treasury.reconciliation.matched.v1'`); n != 3 {
		t.Fatalf("%d matched events in the outbox; want 3", n)
	}
	again, err := f.s.RunReconciliation(f.ctx, RunInput{StatementID: f.statement.ID, Items: f.items}, key)
	if err != nil || len(again.Matches) != 3 || again.Matches[0].ID != res.Matches[0].ID {
		t.Fatalf("replay = %+v, %v; want the stored result", again, err)
	}
	if n := count(t, admin, `select count(*) from reconciliation_match`); n != 3 {
		t.Fatalf("replay stored %d matches; want 3", n)
	}
	fresh, err := f.s.RunReconciliation(f.ctx, RunInput{StatementID: f.statement.ID, Items: f.items}, ids.New())
	if err != nil || len(fresh.Matches) != 0 || fresh.ItemsConsidered != 0 {
		t.Fatalf("second run = %+v, %v; matched items must not be offered again", fresh, err)
	}
	if _, err := f.s.RunReconciliation(f.ctx, RunInput{StatementID: f.statement.ID, Items: f.items[:1]}, key); problemCode(t, err) != CodeIdempotencyReuse {
		t.Fatalf("same key, other body: %v", err)
	}
}

func TestRunReconciliation_ConcurrentRunsMatchEachLineOnce(t *testing.T) {
	f := newReconFixture(t)
	admin := f.admin
	f.addRule(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.s.RunReconciliation(f.ctx, RunInput{StatementID: f.statement.ID, Items: f.items}, ids.New()); err != nil {
				t.Errorf("run: %v", err)
			}
		}()
	}
	wg.Wait()
	if n := count(t, admin, `select count(*) from reconciliation_match where status = 'matched'`); n != 3 {
		t.Fatalf("%d active matches after 4 concurrent runs; want 3", n)
	}
}

func TestRunReconciliation_SuggestsAndClearsStaleSuggestions(t *testing.T) {
	f := newReconFixture(t)
	f.addRule(t)
	near := []RunItem{runItem("ar", "inv-near", "100.00", "INV-2026-0099", "")}
	res, err := f.s.RunReconciliation(f.ctx, RunInput{StatementID: f.statement.ID, Items: near}, ids.New())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 0 || len(res.Suggestions) == 0 || res.Suggestions[0].ItemID != "inv-near" {
		t.Fatalf("result = %+v; want a suggestion for inv-near and no match", res)
	}
	if lineStatuses(t, f)[1] != "suggested" {
		t.Fatalf("line 1 = %s; want suggested", lineStatuses(t, f)[1])
	}
	if _, err := f.s.RunReconciliation(f.ctx, RunInput{StatementID: f.statement.ID, Items: []RunItem{runItem("ar", "far", "9999.00", "X", "")}}, ids.New()); err != nil {
		t.Fatal(err)
	}
	if got := lineStatuses(t, f)[1]; got != "unmatched" {
		t.Fatalf("line 1 = %s after a run with no candidate; want unmatched", got)
	}
}

func TestManualMatch_BalanceReasonDoubleMatchAndUnmatch(t *testing.T) {
	f := newReconFixture(t)
	admin := f.admin
	item := func(id, amount string) MatchItem {
		return MatchItem{Type: "ledger", ID: id, Currency: "EUR", Amount: amount}
	}
	_, err := f.s.CreateManualMatch(f.ctx, NewMatch{StatementID: f.statement.ID, LineNos: []int{4}, Items: []MatchItem{item("je-1", "6.99")}, Reason: "typo"}, ids.New())
	if got := problemCode(t, err); got != CodeMatchUnbalanced {
		t.Fatalf("unbalanced: code %s", got)
	}
	_, err = f.s.CreateManualMatch(f.ctx, NewMatch{StatementID: f.statement.ID, LineNos: []int{9}, Items: []MatchItem{item("je-1", "7.00")}, Reason: "x"}, ids.New())
	if got := problemCode(t, err); got != CodeMatchLine {
		t.Fatalf("missing line: code %s", got)
	}
	foreign := MatchItem{Type: "ledger", ID: "je-usd", Currency: "USD", Amount: "7.00"}
	if _, err = f.s.CreateManualMatch(f.ctx, NewMatch{StatementID: f.statement.ID, LineNos: []int{4}, Items: []MatchItem{foreign}, Reason: "x"}, ids.New()); problemCode(t, err) != CodeMatchLine {
		t.Fatalf("currency mismatch: %v", err)
	}
	m, err := f.s.CreateManualMatch(f.ctx, NewMatch{StatementID: f.statement.ID, LineNos: []int{4}, Items: []MatchItem{item("je-1", "7.00")}, Reason: "bank charge"}, ids.New())
	if err != nil || m.Kind != "manual" || m.Reason != "bank charge" || m.CreatedBy != "user-test" || len(m.Lines) != 1 {
		t.Fatalf("manual match = %+v, %v", m, err)
	}
	if lineStatuses(t, f)[4] != "matched" {
		t.Fatal("line 4 must be matched")
	}
	_, err = f.s.CreateManualMatch(f.ctx, NewMatch{StatementID: f.statement.ID, LineNos: []int{4}, Items: []MatchItem{item("je-2", "7.00")}, Reason: "again"}, ids.New())
	if got := problemCode(t, err); got != CodeLineMatched {
		t.Fatalf("line already matched: code %s", got)
	}
	_, err = f.s.CreateManualMatch(f.ctx, NewMatch{StatementID: f.statement.ID, LineNos: []int{1, 2}, Items: []MatchItem{item("je-1", "150.00")}, Reason: "one item, two lines"}, ids.New())
	if got := problemCode(t, err); got != CodeLineMatched {
		t.Fatalf("item already matched: code %s", got)
	}
	if _, err := f.s.Unmatch(f.ctx, m.ID, m.Version+5, "stale"); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale unmatch: %v", err)
	}
	un, err := f.s.Unmatch(f.ctx, m.ID, m.Version, "wrong entry")
	if err != nil || un.Status != "unmatched" || un.UnmatchReason == nil || *un.UnmatchReason != "wrong entry" || un.Version != m.Version+1 {
		t.Fatalf("unmatch = %+v, %v", un, err)
	}
	if lineStatuses(t, f)[4] != "unmatched" {
		t.Fatal("line 4 must be unmatched again")
	}
	if _, err := f.s.Unmatch(f.ctx, m.ID, un.Version, "twice"); problemCode(t, err) != CodeMatchState {
		t.Fatalf("second unmatch: %v", err)
	}
	if n := count(t, admin, `select count(*) from outbox where type = 'dev.passage.treasury.reconciliation.unmatched.v1'`); n != 1 {
		t.Fatalf("%d unmatched events; want 1", n)
	}
	one2many, err := f.s.CreateManualMatch(f.ctx, NewMatch{StatementID: f.statement.ID, LineNos: []int{1, 2}, Items: []MatchItem{item("je-1", "150.00")}, Reason: "one item, two lines"}, ids.New())
	if err != nil || len(one2many.Lines) != 2 || len(one2many.Items) != 1 {
		t.Fatalf("one item to two lines after the unmatch freed it: %+v, %v", one2many, err)
	}
	list, err := f.s.ListMatches(f.ctx, f.statement.ID)
	if err != nil || len(list) != 2 || list[0].Status != "unmatched" || len(list[1].Lines) != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
}

func TestReconciliation_RulesAreScopedAndUnique(t *testing.T) {
	f := newReconFixture(t)
	f.addRule(t)
	_, err := f.s.CreateReconRule(f.ctx, refRule(f.le, &f.account.ID, "reference", 2), ids.New())
	if got := problemCode(t, err); got != CodeRuleDuplicate {
		t.Fatalf("duplicate name: code %s", got)
	}
	other := ids.New()
	if _, err := f.s.CreateReconRule(f.ctx, refRule(f.le, &other, "ghost", 2), ids.New()); problemCode(t, err) != CodeAccountNotFound {
		t.Fatalf("unknown account: %v", err)
	}
	if _, err := f.s.CreateReconRule(f.ctx, refRule(f.le, nil, "entity-wide", 5), ids.New()); err != nil {
		t.Fatal(err)
	}
	rules, err := f.s.ListReconRules(f.ctx, f.le, f.account.ID)
	if err != nil || len(rules) != 2 || rules[0].Name != "reference" || rules[1].Name != "entity-wide" {
		t.Fatalf("rules = %+v, %v; want priority order", rules, err)
	}
	key := ids.New()
	a, err := f.s.CreateReconRule(f.ctx, refRule(f.le, nil, "idem", 6), key)
	b, err2 := f.s.CreateReconRule(f.ctx, refRule(f.le, nil, "idem", 6), key)
	if err != nil || err2 != nil || a.ID != b.ID {
		t.Fatalf("idempotent create: %+v %+v %v %v", a, b, err, err2)
	}
}

func TestReconciliation_OtherTenantAndOtherEntityCannotSeeOrUseMatches(t *testing.T) {
	f := newReconFixture(t)
	f.addRule(t)
	res, err := f.s.RunReconciliation(f.ctx, RunInput{StatementID: f.statement.ID, Items: f.items}, ids.New())
	if err != nil || len(res.Matches) == 0 {
		t.Fatalf("run: %+v %v", res, err)
	}
	for name, ctx := range map[string]context.Context{"other tenant": ctxFor(2, ""), "other legal entity": ctxFor(1, ids.New())} {
		if _, err := f.s.GetMatch(ctx, res.Matches[0].ID); problemCode(t, err) != CodeMatchNotFound {
			t.Errorf("%s: GetMatch err = %v", name, err)
		}
		if _, err := f.s.RunReconciliation(ctx, RunInput{StatementID: f.statement.ID, Items: f.items}, ids.New()); problemCode(t, err) != CodeStatementNotFound {
			t.Errorf("%s: run err = %v", name, err)
		}
		if _, err := f.s.Unmatch(ctx, res.Matches[0].ID, 1, "no"); problemCode(t, err) != CodeMatchNotFound {
			t.Errorf("%s: unmatch err = %v", name, err)
		}
		if rules, err := f.s.ListReconRules(ctx, f.le, ""); err != nil || len(rules) != 0 {
			t.Errorf("%s: rules = %+v, %v", name, rules, err)
		}
	}
}
