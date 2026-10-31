package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

// oiSetup adds a clearing account 1999 (open-item managed) and a difference
// account 6990, a reason code for resets and a tolerance group STD of 0.05.
func oiSetup(t *testing.T) env {
	t.Helper()
	e, _ := setup(t)
	for num, a := range map[string]Account{
		"1999": {Name: "Clearing", Type: "asset", Postable: true},
		"6990": {Name: "Clearing differences", Type: "expense", Postable: true},
	} {
		if _, _, err := e.store.PutAccount(e.ctx, "STD", num, a); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := e.store.PutReasonCode(e.ctx, "CORR", ReasonCode{Name: "Correction", AppliesTo: []string{"ledger"}, AlternativePostingDateAllowed: true, Active: true}); err != nil {
		t.Fatal(err)
	}
	if _, created, err := e.store.PutOpenItemAccount(e.ctx, e.ledger.ID, "1999", true); err != nil || !created {
		t.Fatalf("enable: created=%v err=%v", created, err)
	}
	return e
}

// tolerance puts a group with the same limit on both sides.
func tolerance(t *testing.T, e env, code, abs, pct string) {
	t.Helper()
	if _, _, err := e.store.PutToleranceGroup(e.ctx, e.ledger.ID, code, 0, ToleranceGroup{Name: code, DebitLimit: abs, CreditLimit: abs, PercentLimit: pct, DifferenceAccount: "6990", Active: true}); err != nil {
		t.Fatal(err)
	}
}

// postOn posts amount on account 1999 against another account on the given side.
func postOn(t *testing.T, e env, key, side, amount, ref string, date time.Time) {
	t.Helper()
	other := domain.SideCredit
	counter := "2100"
	if side == domain.SideCredit {
		other, counter = domain.SideDebit, "6010"
	}
	hash, _ := domain.HashPayload(map[string]string{"key": key})
	a := decimal.RequireFromString(amount)
	_, err := e.store.PostEntry(e.ctx, PostInput{LedgerID: e.ledger.ID, DocumentID: ids.New(), PostingDate: date, Description: key,
		Source: ledgerevents.Source{System: "ledger", Type: "test", ID: key},
		Lines: []domain.LineInput{
			{Account: "1999", Side: side, Amount: a, Currency: "USD", SourceLineRef: ref},
			{Account: counter, Side: other, Amount: a, Currency: "USD"},
		}, Key: key, Hash: hash, Actor: "test-user", TraceID: testTrace, Today: testToday})
	if err != nil {
		t.Fatalf("post %s: %v", key, err)
	}
}

func openItems(t *testing.T, e env, status string) []OpenItem {
	t.Helper()
	page, err := e.store.ListOpenItems(e.ctx, OpenItemFilter{LedgerID: e.ledger.ID, Account: "1999", Status: status})
	if err != nil {
		t.Fatal(err)
	}
	return page.Items
}

func clearingOf(e env, key string, items []OpenItem, group string, residual bool, date time.Time) ClearingInput {
	var ids []string
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	hash, _ := domain.HashPayload(map[string]any{"ids": ids, "group": group, "residual": residual, "date": date.String()})
	return ClearingInput{LedgerID: e.ledger.ID, ItemIDs: ids, ClearingDate: date, ToleranceGroup: group, ResidualItem: residual,
		Key: key, Hash: hash, TraceID: testTrace, Today: testToday}
}

func clearingEvents(t *testing.T, e env, docID string) []string {
	t.Helper()
	var out []string
	err := dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(e.ctx, `select payload->'data'->>'status' from outbox where type = $1 and aggregate_id = $2 and aggregate_type = 'clearing' order by id`,
			ledgerevents.OpenItemCleared, docID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return err
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func trialNet(t *testing.T, e env, account string) string {
	t.Helper()
	tb, err := e.store.TrialBalance(e.ctx, BalanceFilter{LedgerID: e.ledger.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range tb.Rows {
		if r.Account == account {
			return r.Net
		}
	}
	return "absent"
}

// AC-11.12: 100.00 D and 99.98 C within a user tolerance of 0.05 clear with a
// 0.02 difference line to the tolerance account; with 0.01 and a residual item
// the 0.02 becomes a new open item instead.
func TestPostgres_AC11_12_ManualClearingWithToleranceAndResidual(t *testing.T) {
	e := oiSetup(t)
	tolerance(t, e, "STD", "0.05", "0")
	tolerance(t, e, "TIGHT", "0.01", "0")
	postOn(t, e, "inv-1", domain.SideDebit, "100.00", "PO-7", testPostingDate)
	postOn(t, e, "gr-1", domain.SideCredit, "99.98", "PO-7", testPostingDate)

	items := openItems(t, e, "open")
	if len(items) != 2 || items[0].Amount != "100.00" || items[0].DebitCredit != "D" || items[0].Assignment != "PO-7" || items[1].DebitCredit != "C" ||
		items[0].Voucher == nil || items[0].EntryID == nil {
		t.Fatalf("items: %+v", items)
	}
	clearDate := testPostingDate.AddDate(0, 0, 1)

	pv, err := e.store.SimulateClearing(e.ctx, clearingOf(e, "", items, "STD", false, clearDate))
	if err != nil || pv.Outcome != domain.OutcomeDifference || pv.Net != "0.02" || pv.DifferenceAmount != "0.02" || pv.Debit != "100.00" || pv.Credit != "99.98" {
		t.Fatalf("simulate: %+v err=%v", pv, err)
	}
	if got := openItems(t, e, "open"); len(got) != 2 {
		t.Fatal("a simulation must not change anything")
	}

	doc, created, err := e.store.ClearItems(e.ctx, clearingOf(e, "clear-1", items, "STD", false, clearDate))
	if err != nil || !created || doc.Status != "cleared" || doc.DifferenceAmount != "0.02" || doc.DifferenceEntryID == nil || len(doc.ItemIDs) != 2 || doc.Account != "1999" {
		t.Fatalf("clear: %+v created=%v err=%v", doc, created, err)
	}
	if got := openItems(t, e, "open"); len(got) != 0 {
		t.Fatalf("nothing may stay open: %+v", got)
	}
	if got := openItems(t, e, "cleared"); len(got) != 3 {
		t.Fatalf("two items and the difference item are cleared: %d", len(got))
	}
	if net := trialNet(t, e, "1999"); net != "0.00" {
		t.Fatalf("clearing account net after the write-off: %s", net)
	}
	if net := trialNet(t, e, "6990"); net != "0.02" {
		t.Fatalf("difference account net: %s", net)
	}
	diff, err := e.store.GetEntry(e.ctx, *doc.DifferenceEntryID)
	if err != nil || diff.Source.Type != "clearing_difference" || diff.Lines[0].DebitCredit != "C" || diff.Lines[0].Amount != "0.02" || diff.Lines[1].Account != "6990" {
		t.Fatalf("difference entry: %+v err=%v", diff, err)
	}

	// The same key replays the document; the same key with another body conflicts.
	again, created, err := e.store.ClearItems(e.ctx, clearingOf(e, "clear-1", items, "STD", false, clearDate))
	if err != nil || created || again.ID != doc.ID {
		t.Fatalf("replay: %+v created=%v err=%v", again, created, err)
	}
	if _, _, err := e.store.ClearItems(e.ctx, clearingOf(e, "clear-1", items, "TIGHT", false, clearDate)); problemCode(err) != "LEDGER_IDEMPOTENCY_CONFLICT" {
		t.Fatalf("conflict: %v", err)
	}
	// A cleared item cannot be in a second active clearing (BR-11.13).
	if _, _, err := e.store.ClearItems(e.ctx, clearingOf(e, "clear-2", items, "STD", false, clearDate)); problemCode(err) != CodeOpenItemCleared {
		t.Fatalf("already cleared: %v", err)
	}

	got, err := e.store.GetClearing(e.ctx, doc.ID)
	if err != nil || len(got.Items) != 2 || got.Items[0].Status != ItemCleared || got.Items[0].ClearingDocumentID == nil {
		t.Fatalf("get: %+v err=%v", got, err)
	}
	if ev := clearingEvents(t, e, doc.ID); len(ev) != 1 || ev[0] != "cleared" {
		t.Fatalf("events: %v", ev)
	}

	// Reset as a whole: the difference entry is reversed (reason code needed), items reopen.
	if _, err := e.admin.Exec(context.Background(), `update ledger set reversal_reason_required = true where id = $1`, e.ledger.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.ResetClearing(e.ctx, ResetInput{ID: doc.ID, Key: "reset-1", TraceID: testTrace, Today: testToday}); problemCode(err) != "LEDGER_REASON_REQUIRED" && !strings.Contains(errText(err), "LEDGER_REASON_REQUIRED") {
		t.Fatalf("a reversal without a reason: %v", err)
	}
	reset, err := e.store.ResetClearing(e.ctx, ResetInput{ID: doc.ID, Key: "reset-1", ReasonCode: "CORR", Comment: "wrong tolerance", TraceID: testTrace, Today: testToday})
	if err != nil || reset.Status != "reset" || reset.ResetReasonCode == nil || *reset.ResetReasonCode != "CORR" || reset.ResetBy == nil {
		t.Fatalf("reset: %+v err=%v", reset, err)
	}
	reopened := openItems(t, e, "open")
	if len(reopened) != 2 || reopened[0].Status != ItemResetOpen || reopened[0].ClearingDocumentID != nil {
		t.Fatalf("after reset: %+v", reopened)
	}
	if got := openItems(t, e, "voided"); len(got) != 2 {
		t.Fatalf("the difference item and its reversal are voided: %d", len(got))
	}
	if net := trialNet(t, e, "6990"); net != "0.00" {
		t.Fatalf("difference account after reset: %s", net)
	}
	if again, err := e.store.ResetClearing(e.ctx, ResetInput{ID: doc.ID, Key: "reset-1", ReasonCode: "CORR", TraceID: testTrace, Today: testToday}); err != nil || again.Status != "reset" {
		t.Fatalf("reset replay: %+v err=%v", again, err)
	}
	if _, err := e.store.ResetClearing(e.ctx, ResetInput{ID: doc.ID, Key: "reset-2", ReasonCode: "CORR", TraceID: testTrace, Today: testToday}); problemCode(err) != CodeClearingAlreadyReset {
		t.Fatalf("second reset: %v", err)
	}
	if ev := clearingEvents(t, e, doc.ID); len(ev) != 2 || ev[1] != "reset" {
		t.Fatalf("events after reset: %v", ev)
	}

	// Cleared again by a new document, now with the tight tolerance and a residual item.
	if _, _, err := e.store.ClearItems(e.ctx, clearingOf(e, "clear-3", reopened, "TIGHT", false, clearDate)); !strings.Contains(errText(err), CodeClearingUnbalanced) {
		t.Fatalf("above tolerance without a residual item: %v", err)
	}
	doc2, _, err := e.store.ClearItems(e.ctx, clearingOf(e, "clear-4", reopened, "TIGHT", true, clearDate))
	if err != nil || doc2.ResidualItemID == nil || doc2.DifferenceEntryID != nil || doc2.DifferenceAmount != "0.00" {
		t.Fatalf("residual: %+v err=%v", doc2, err)
	}
	open := openItems(t, e, "open")
	if len(open) != 1 || open[0].Amount != "0.02" || open[0].DebitCredit != "D" || open[0].SourceType != "residual" || open[0].ResidualOfItemID == nil || *open[0].ResidualOfItemID != reopened[0].ID {
		t.Fatalf("residual item: %+v", open)
	}
	// A residual that was cleared since blocks the reset of the clearing that made it.
	postOn(t, e, "cn-1", domain.SideCredit, "0.02", "PO-7", testPostingDate)
	doc3, _, err := e.store.ClearItems(e.ctx, clearingOf(e, "clear-5", openItems(t, e, "open"), "", false, clearDate))
	if err != nil {
		t.Fatalf("clear the residual: %v", err)
	}
	if _, err := e.store.ResetClearing(e.ctx, ResetInput{ID: doc2.ID, Key: "reset-3", ReasonCode: "CORR", TraceID: testTrace, Today: testToday}); problemCode(err) != CodeClearingResidualUsed {
		t.Fatalf("reset with a cleared residual: %v", err)
	}
	if _, err := e.store.ResetClearing(e.ctx, ResetInput{ID: doc3.ID, Key: "reset-4", ReasonCode: "CORR", TraceID: testTrace, Today: testToday}); err != nil {
		t.Fatalf("reset the residual clearing: %v", err)
	}
	if _, err := e.store.ResetClearing(e.ctx, ResetInput{ID: doc2.ID, Key: "reset-3", ReasonCode: "CORR", TraceID: testTrace, Today: testToday}); err != nil {
		t.Fatalf("reset with an open residual: %v", err)
	}
	if got := openItems(t, e, "voided"); len(got) != 3 {
		t.Fatalf("the residual item is voided: %d", len(got))
	}
	if got := openItems(t, e, "open"); len(got) != 3 {
		t.Fatalf("the two items and the credit note are open again: %d", len(got))
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestPostgres_ClearingRules(t *testing.T) {
	e := oiSetup(t)
	tolerance(t, e, "STD", "0.05", "0")
	postOn(t, e, "inv-1", domain.SideDebit, "50.00", "", testPostingDate)
	postOn(t, e, "gr-1", domain.SideCredit, "50.00", "", testPostingDate.AddDate(0, 0, 5))
	items := openItems(t, e, "open")

	// A date before the latest posting date of the items is refused (BR-11.13).
	if _, _, err := e.store.ClearItems(e.ctx, clearingOf(e, "c1", items, "", false, testPostingDate)); !strings.Contains(errText(err), CodeClearingDateInvalid) {
		t.Fatalf("date: %v", err)
	}
	// Items of a ledger the caller cannot see, or that do not exist, are 404.
	ghost := []OpenItem{{ID: ids.New()}, {ID: items[0].ID}}
	if _, _, err := e.store.ClearItems(e.ctx, clearingOf(e, "c2", ghost, "", false, testToday)); problemCode(err) != CodeOpenItemNotFound {
		t.Fatalf("missing item: %v", err)
	}
	if _, _, err := e.store.ClearItems(e.ctx, clearingOf(e, "c3", items, "NOPE", false, testToday)); problemCode(err) != CodeToleranceNotFound {
		t.Fatalf("unknown group: %v", err)
	}
	if _, _, err := e.store.PutToleranceGroup(e.ctx, e.ledger.ID, "OLD", 0, ToleranceGroup{Name: "Old", DebitLimit: "1", CreditLimit: "1", PercentLimit: "0", DifferenceAccount: "6990", Active: false}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.store.ClearItems(e.ctx, clearingOf(e, "c4", items, "OLD", false, testToday)); !strings.Contains(errText(err), CodeToleranceInactive) {
		t.Fatalf("inactive group: %v", err)
	}
	// An exact match needs no tolerance and no difference entry.
	doc, created, err := e.store.ClearItems(e.ctx, clearingOf(e, "c5", items, "", false, testToday))
	if err != nil || !created || doc.DifferenceEntryID != nil || doc.ResidualItemID != nil || doc.DifferenceAmount != "0.00" {
		t.Fatalf("exact clearing: %+v err=%v", doc, err)
	}
	// An account without open-item management has no items to clear.
	hash, _ := domain.HashPayload("x")
	if _, _, err := e.store.ClearItems(e.ctx, ClearingInput{LedgerID: e.ledger.ID, ItemIDs: []string{ids.New()}, ClearingDate: testToday, Key: "c6", Hash: hash, Today: testToday}); !strings.Contains(errText(err), "COM-0001") {
		t.Fatalf("one item: %v", err)
	}
	if got := listClearings(t, e, ""); len(got) != 1 || got[0].ID != doc.ID {
		t.Fatalf("list: %+v", got)
	}
	if got := listClearings(t, e, "reset"); len(got) != 0 {
		t.Fatalf("list reset: %+v", got)
	}
	// Tolerance group versioning.
	g, _, err := e.store.PutToleranceGroup(e.ctx, e.ledger.ID, "STD", 1, ToleranceGroup{Name: "Standard", DebitLimit: "0.10", CreditLimit: "0.02", PercentLimit: "1.5", DifferenceAccount: "6990", Active: true})
	if err != nil || g.Version != 2 || g.DebitLimit != "0.100000" || g.CreditLimit != "0.020000" {
		t.Fatalf("replace: %+v err=%v", g, err)
	}
	if _, _, err := e.store.PutToleranceGroup(e.ctx, e.ledger.ID, "STD", 1, ToleranceGroup{Name: "Stale", DebitLimit: "0.10", CreditLimit: "0.10", PercentLimit: "0", DifferenceAccount: "6990", Active: true}); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale: %v", err)
	}
	if _, _, err := e.store.PutToleranceGroup(e.ctx, e.ledger.ID, "BAD", 0, ToleranceGroup{Name: "Bad", DebitLimit: "1", CreditLimit: "1", PercentLimit: "0", DifferenceAccount: "0000", Active: true}); !strings.Contains(errText(err), CodeAccountNotFound) {
		t.Fatalf("unknown difference account: %v", err)
	}
}

func listClearings(t *testing.T, e env, status string) []ClearingDocument {
	t.Helper()
	got, err := e.store.ListClearings(e.ctx, e.ledger.ID, status)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPostgres_OpenItemAccountRules(t *testing.T) {
	e := oiSetup(t)
	// Vendor reconciliation accounts and P&L accounts are not eligible (BR-11.18).
	for _, num := range []string{"2110", "6010"} {
		if _, _, err := e.store.PutOpenItemAccount(e.ctx, e.ledger.ID, num, true); !strings.Contains(errText(err), CodeOIAccountInvalid) {
			t.Fatalf("%s: %v", num, err)
		}
	}
	if _, _, err := e.store.PutOpenItemAccount(e.ctx, e.ledger.ID, "9999", true); problemCode(err) != CodeAccountNotFound {
		t.Fatalf("unknown account: %v", err)
	}
	// An account with a balance needs an activation run.
	if _, _, err := e.store.PutAccount(e.ctx, "STD", "1100", Account{Name: "Bank", Type: "asset", Postable: true}); err != nil {
		t.Fatal(err)
	}
	hash, _ := domain.HashPayload("bank")
	if _, err := e.store.PostEntry(e.ctx, PostInput{LedgerID: e.ledger.ID, DocumentID: ids.New(), PostingDate: testPostingDate, Description: "bank",
		Source: ledgerevents.Source{System: "ledger", Type: "test", ID: "bank"},
		Lines: []domain.LineInput{
			{Account: "1100", Side: domain.SideDebit, Amount: decimal.NewFromInt(10), Currency: "USD"},
			{Account: "2100", Side: domain.SideCredit, Amount: decimal.NewFromInt(10), Currency: "USD"}},
		Key: "bank", Hash: hash, Actor: "test-user", TraceID: testTrace, Today: testToday}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.store.PutOpenItemAccount(e.ctx, e.ledger.ID, "1100", true); !strings.Contains(errText(err), CodeOIActivationBalance) {
		t.Fatalf("balance: %v", err)
	}
	// Lines on accounts that are not managed make no items.
	if got := openItems(t, e, ""); len(got) != 0 {
		t.Fatalf("items: %+v", got)
	}
	// Switching off needs zero open items.
	postOn(t, e, "inv-1", domain.SideDebit, "5.00", "", testPostingDate)
	if _, _, err := e.store.PutOpenItemAccount(e.ctx, e.ledger.ID, "1999", false); problemCode(err) != CodeOIItemsExist {
		t.Fatalf("switch off with items: %v", err)
	}
	list, err := e.store.ListOpenItemAccounts(e.ctx, e.ledger.ID)
	if err != nil || len(list) != 1 || list[0].Account != "1999" || !list[0].Enabled || list[0].OpenItems != 1 {
		t.Fatalf("list: %+v err=%v", list, err)
	}
}

func TestPostgres_OpenItemsAreTenantScopedAndPaged(t *testing.T) {
	e := oiSetup(t)
	for i := 0; i < 3; i++ {
		postOn(t, e, "inv-"+string(rune('a'+i)), domain.SideDebit, "1.00", "", testPostingDate)
	}
	page, err := e.store.ListOpenItems(e.ctx, OpenItemFilter{LedgerID: e.ledger.ID, Limit: 2})
	if err != nil || len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("page 1: %+v err=%v", page, err)
	}
	rest, err := e.store.ListOpenItems(e.ctx, OpenItemFilter{LedgerID: e.ledger.ID, Limit: 2, Cursor: page.NextCursor})
	if err != nil || len(rest.Items) != 1 || rest.NextCursor != "" || rest.Items[0].ID == page.Items[0].ID {
		t.Fatalf("page 2: %+v err=%v", rest, err)
	}
	byDim, err := e.store.ListOpenItems(e.ctx, OpenItemFilter{LedgerID: e.ledger.ID, Dimensions: map[string]string{"DEPT": "D100"}})
	if err != nil || len(byDim.Items) != 0 {
		t.Fatalf("dimension filter: %+v err=%v", byDim, err)
	}
	other := principalCtx("0190a000-0000-7000-8000-0000000000ee", e.le)
	if _, err := e.store.ListOpenItems(other, OpenItemFilter{LedgerID: e.ledger.ID}); problemCode(err) != "LEDGER_NOT_FOUND" {
		t.Fatalf("another tenant must not see the ledger: %v", err)
	}
	var visible int
	if err := dbx.WithTenantTx(other, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(other, `select count(*) from open_item`).Scan(&visible)
	}); err != nil || visible != 0 {
		t.Fatalf("row level security: %d rows err=%v", visible, err)
	}
}

// Clearing integrity: a cleared item's entry cannot be reversed outside a reset,
// a reset always names a reason, the tolerance applies per side, the difference
// account may not be open-item managed, items of two accounts do not mix, and
// another legal entity cannot see the clearing.
func TestPostgres_ClearingIntegrity(t *testing.T) {
	e := oiSetup(t)
	if _, _, err := e.store.PutAccount(e.ctx, "STD", "1998", Account{Name: "Suspense", Type: "asset", Postable: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.store.PutOpenItemAccount(e.ctx, e.ledger.ID, "1998", true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.store.PutToleranceGroup(e.ctx, e.ledger.ID, "SIDED", 0, ToleranceGroup{Name: "Sided", DebitLimit: "0.05", CreditLimit: "0.01", PercentLimit: "0", DifferenceAccount: "6990", Active: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.store.PutToleranceGroup(e.ctx, e.ledger.ID, "OIDIFF", 0, ToleranceGroup{Name: "Managed difference", DebitLimit: "1", CreditLimit: "1", PercentLimit: "0", DifferenceAccount: "1998", Active: true}); err != nil {
		t.Fatal(err)
	}
	postOn(t, e, "inv-1", domain.SideDebit, "99.98", "", testPostingDate)
	postOn(t, e, "gr-1", domain.SideCredit, "100.00", "", testPostingDate)
	items := openItems(t, e, "open")

	// A credit residual of 0.02 is above the 0.01 credit limit although the debit limit is 0.05.
	pv, err := e.store.SimulateClearing(e.ctx, clearingOf(e, "", items, "SIDED", false, testToday))
	if err != nil || pv.Outcome != domain.OutcomeUnbalanced || pv.Net != "-0.02" {
		t.Fatalf("credit residual: %+v err=%v", pv, err)
	}
	if _, _, err := e.store.ClearItems(e.ctx, clearingOf(e, "c1", items, "OIDIFF", false, testToday)); !strings.Contains(errText(err), CodeClearingDifferenceAccount) {
		t.Fatalf("managed difference account: %v", err)
	}
	// Items of two accounts cannot be cleared together.
	hash, _ := domain.HashPayload("suspense")
	if _, err := e.store.PostEntry(e.ctx, PostInput{LedgerID: e.ledger.ID, DocumentID: ids.New(), PostingDate: testPostingDate, Description: "suspense",
		Source: ledgerevents.Source{System: "ledger", Type: "test", ID: "suspense"},
		Lines: []domain.LineInput{
			{Account: "1998", Side: domain.SideDebit, Amount: decimal.NewFromInt(3), Currency: "USD"},
			{Account: "2100", Side: domain.SideCredit, Amount: decimal.NewFromInt(3), Currency: "USD"}},
		Key: "suspense", Hash: hash, Actor: "test-user", TraceID: testTrace, Today: testToday}); err != nil {
		t.Fatal(err)
	}
	all, err := e.store.ListOpenItems(e.ctx, OpenItemFilter{LedgerID: e.ledger.ID, Status: "open"})
	if err != nil || len(all.Items) != 3 {
		t.Fatalf("all items: %+v err=%v", all, err)
	}
	if _, _, err := e.store.ClearItems(e.ctx, clearingOf(e, "c2", all.Items, "", true, testToday)); !strings.Contains(errText(err), CodeClearingAccountMixed) {
		t.Fatalf("mixed accounts: %v", err)
	}

	doc, _, err := e.store.ClearItems(e.ctx, clearingOf(e, "c3", items, "", true, testToday))
	if err != nil || doc.ResidualItemID == nil {
		t.Fatalf("clear with residual: %+v err=%v", doc, err)
	}
	// The entry of a cleared item cannot be reversed until the clearing is reset.
	revHash, _ := domain.HashPayload("rev")
	rev := ReverseInput{EntryID: *items[0].EntryID, Key: "rev-1", Hash: revHash, Actor: "test-user", TraceID: testTrace, Today: testToday, CanPostLedgerSpecific: true}
	if _, err := e.store.ReverseEntry(e.ctx, rev); problemCode(err) != CodeOpenItemCleared {
		t.Fatalf("reverse a cleared entry: %v", err)
	}
	// Another legal entity of the tenant does not see the document.
	other := principalCtx(e.tenant, ids.New())
	if _, err := e.store.GetClearing(other, doc.ID); problemCode(err) != CodeClearingNotFound {
		t.Fatalf("legal-entity scope: %v", err)
	}
	if _, err := e.store.ResetClearing(other, ResetInput{ID: doc.ID, Key: "r0", ReasonCode: "CORR", Today: testToday}); problemCode(err) != CodeClearingNotFound {
		t.Fatalf("legal-entity scope on reset: %v", err)
	}
	// A reset without a difference entry still needs an active ledger reason.
	if _, err := e.store.ResetClearing(e.ctx, ResetInput{ID: doc.ID, Key: "r1", Today: testToday}); !strings.Contains(errText(err), "LEDGER_REASON_REQUIRED") {
		t.Fatalf("reset without a reason: %v", err)
	}
	if _, err := e.store.ResetClearing(e.ctx, ResetInput{ID: doc.ID, Key: "r1", ReasonCode: "NOPE", Today: testToday}); !strings.Contains(errText(err), "LEDGER_REASON_REQUIRED") {
		t.Fatalf("reset with an unknown reason: %v", err)
	}
	if _, err := e.store.ResetClearing(e.ctx, ResetInput{ID: doc.ID, Key: "r1", ReasonCode: "CORR", Today: testToday}); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := e.store.ReverseEntry(e.ctx, rev); err != nil {
		t.Fatalf("reverse after the reset: %v", err)
	}
}

// BR-11.13: a residual item's residual_of_item_id chain is at most 20 deep.
func TestPostgres_ResidualChainIsAtMost20Deep(t *testing.T) {
	e := oiSetup(t)
	postOn(t, e, "inv-0", domain.SideDebit, "1.00", "", testPostingDate)
	for round := 1; ; round++ {
		key := "cn-" + strings.Repeat("x", round)
		postOn(t, e, key, domain.SideCredit, "0.01", "", testPostingDate)
		_, _, err := e.store.ClearItems(e.ctx, clearingOf(e, key, openItems(t, e, "open"), "", true, testToday))
		// The first item starts the chain (depth 1); round n makes a residual at depth n+1.
		if round < domain.MaxResidualDepth {
			if err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
			continue
		}
		if !strings.Contains(errText(err), CodeClearingResidualDepth) {
			t.Fatalf("round %d must exceed the chain: %v", round, err)
		}
		break
	}
	if got := openItems(t, e, "open"); len(got) != 2 || got[0].Amount != "0.81" {
		t.Fatalf("the last residual and the refused credit stay open: %+v", got)
	}
}
