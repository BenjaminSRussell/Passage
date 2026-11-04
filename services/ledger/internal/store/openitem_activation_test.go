package store

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

// postHistory posts amount on account 1999 against 2100 with a DEPT value on the 1999 line.
func postHistory(t *testing.T, e env, key, side, amount, dept string) {
	t.Helper()
	if err := postHistoryOn(e, key, side, amount, dept, testPostingDate); err != nil {
		t.Fatalf("post %s: %v", key, err)
	}
}

func postHistoryOn(e env, key, side, amount, dept string, date time.Time) error {
	other, counter := domain.SideCredit, "2100"
	if side == domain.SideCredit {
		other, counter = domain.SideDebit, "6010"
	}
	hash, _ := domain.HashPayload(map[string]string{"key": key})
	a := decimal.RequireFromString(amount)
	line := domain.LineInput{Account: "1999", Side: side, Amount: a, Currency: "USD"}
	if dept != "" {
		line.Dimensions = map[string]string{"DEPT": dept}
	}
	_, err := e.store.PostEntry(e.ctx, PostInput{LedgerID: e.ledger.ID, DocumentID: ids.New(), PostingDate: date, Description: key,
		Source: ledgerevents.Source{System: "ledger", Type: "test", ID: key},
		Lines:  []domain.LineInput{line, {Account: counter, Side: other, Amount: a, Currency: "USD"}},
		Key:    key, Hash: hash, Actor: "test-user", TraceID: testTrace, Today: testToday})
	return err
}

func activationOn(e env, key, account string, date time.Time) ActivationInput {
	hash, _ := domain.HashPayload(map[string]string{"account": account, "date": date.String()})
	return ActivationInput{LedgerID: e.ledger.ID, Account: account, Date: date, Key: key, Hash: hash}
}

// openNet is the signed (debit positive) sum of the open items on account 1999.
func openNet(t *testing.T, e env) string {
	t.Helper()
	sum := decimal.Zero
	for _, it := range openItems(t, e, "open") {
		a := decimal.RequireFromString(it.Amount)
		if it.DebitCredit == domain.SideCredit {
			a = a.Neg()
		}
		sum = sum.Add(a)
	}
	return sum.StringFixed(2)
}

func activation(e env, key, account string) ActivationInput {
	hash, _ := domain.HashPayload(map[string]string{"account": account, "date": testPostingDate.String()})
	return ActivationInput{LedgerID: e.ledger.ID, Account: account, Date: testPostingDate, Key: key, Hash: hash}
}

// An account with history is refused by the plain switch and activated by a run that
// carries each non-zero combination balance in as one opening item.
func TestPostgres_ActivationRunCarriesBalancesIn(t *testing.T) {
	e, _ := setup(t)
	if _, _, err := e.store.PutAccount(e.ctx, "STD", "1999", 0, Account{Name: "Clearing", Type: "asset", Postable: true}); err != nil {
		t.Fatal(err)
	}
	postHistory(t, e, "h1", domain.SideDebit, "100.00", "D100")
	postHistory(t, e, "h2", domain.SideDebit, "50.00", "D100")
	postHistory(t, e, "h3", domain.SideCredit, "30.00", "D200")
	postHistory(t, e, "h4", domain.SideDebit, "20.00", "D300")
	postHistory(t, e, "h5", domain.SideCredit, "20.00", "D300") // nets to zero: no item
	postHistory(t, e, "h6", domain.SideDebit, "7.00", "")

	if _, _, err := e.store.PutOpenItemAccount(e.ctx, e.ledger.ID, "1999", true); !strings.Contains(errText(err), CodeOIActivationBalance) {
		t.Fatalf("plain switch must need an activation run: %v", err)
	}
	run, replayed, err := e.store.ActivateOpenItemAccount(e.ctx, activation(e, "act-1", "1999"))
	if err != nil || replayed {
		t.Fatalf("activate: replayed=%v err=%v", replayed, err)
	}
	if run.ItemsCreated != 3 || run.TotalDebit != "157.00" || run.TotalCredit != "30.00" || run.Account != "1999" || run.ActivationDate != testPostingDate.Format("2006-01-02") {
		t.Fatalf("run: %+v", run)
	}
	items := openItems(t, e, "open")
	if len(items) != 3 {
		t.Fatalf("items: %+v", items)
	}
	got := map[string]string{}
	for _, it := range items {
		if it.SourceType != "activation" || it.Assignment != "ACTIVATION" || it.EntryID != nil || it.PostingDate != testPostingDate.Format("2006-01-02") {
			t.Fatalf("item: %+v", it)
		}
		got[it.Dimensions["DEPT"]] = it.DebitCredit + it.Amount
	}
	if got["D100"] != "D150.00" || got["D200"] != "C30.00" || got[""] != "D7.00" {
		t.Fatalf("amounts by DEPT: %v", got)
	}
	list, err := e.store.ListOpenItemAccounts(e.ctx, e.ledger.ID)
	if err != nil || len(list) != 1 || !list[0].Enabled || list[0].ActivationRunID != run.ID || list[0].OpenItems != 3 {
		t.Fatalf("setting: %+v err=%v", list, err)
	}
	// Later lines make items the usual way.
	postHistory(t, e, "h7", domain.SideDebit, "1.00", "D100")
	if got := openItems(t, e, "open"); len(got) != 4 {
		t.Fatalf("a new line must make an item: %d", len(got))
	}
	// Replay and conflict.
	again, replayed, err := e.store.ActivateOpenItemAccount(e.ctx, activation(e, "act-1", "1999"))
	if err != nil || !replayed || again.ID != run.ID {
		t.Fatalf("replay: %+v replayed=%v err=%v", again, replayed, err)
	}
	other := activation(e, "act-1", "1999")
	other.Hash = activation(e, "x", "2100").Hash
	if _, _, err := e.store.ActivateOpenItemAccount(e.ctx, other); problemCode(err) != CodeIdempotencyConflict {
		t.Fatalf("changed body: %v", err)
	}
	if _, _, err := e.store.ActivateOpenItemAccount(e.ctx, activation(e, "act-2", "1999")); problemCode(err) != CodeOIAlreadyActive {
		t.Fatalf("second activation: %v", err)
	}
	runs, err := e.store.ListActivationRuns(e.ctx, e.ledger.ID)
	if err != nil || len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("runs: %+v err=%v", runs, err)
	}
	// The event and the books: one event, and the trial balance of the account is untouched.
	var events int
	if err := dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `select count(*)::int from outbox where type = $1 and aggregate_id = $2 and aggregate_type = 'open_item_activation'`,
			ledgerevents.OpenItemActivated, run.ID).Scan(&events)
	}); err != nil || events != 1 {
		t.Fatalf("events: %d err=%v", events, err)
	}
	if net := trialNet(t, e, "1999"); net != "128.00" {
		t.Fatalf("books must not change, net %s", net)
	}
}

func TestPostgres_ActivationRunRules(t *testing.T) {
	e, _ := setup(t)
	for num, a := range map[string]Account{
		"1999": {Name: "Clearing", Type: "asset", Postable: true},
	} {
		if _, _, err := e.store.PutAccount(e.ctx, "STD", num, 0, a); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := e.store.ActivateOpenItemAccount(e.ctx, activation(e, "k1", "9999")); problemCode(err) != CodeAccountNotFound {
		t.Fatalf("unknown account: %v", err)
	}
	for _, acc := range []string{"6010", "2110"} { // expense, vendor reconciliation
		if _, _, err := e.store.ActivateOpenItemAccount(e.ctx, activation(e, "k-"+acc, acc)); !strings.Contains(errText(err), CodeOIAccountInvalid) {
			t.Fatalf("%s must not qualify: %v", acc, err)
		}
	}
	// An account without history activates with zero items.
	run, _, err := e.store.ActivateOpenItemAccount(e.ctx, activation(e, "k2", "1999"))
	if err != nil || run.ItemsCreated != 0 || run.TotalDebit != "0.00" {
		t.Fatalf("empty activation: %+v err=%v", run, err)
	}
	// A liability account qualifies as well.
	if _, _, err := e.store.ActivateOpenItemAccount(e.ctx, activation(e, "k-2100", "2100")); err != nil {
		t.Fatalf("a liability account qualifies: %v", err)
	}
	other := principalCtx("0190a000-0000-7000-8000-0000000000ee", e.le)
	if _, err := e.store.ListActivationRuns(other, e.ledger.ID); problemCode(err) != "LEDGER_NOT_FOUND" {
		t.Fatalf("another tenant must not see the ledger: %v", err)
	}
	var visible int
	if err := dbx.WithTenantTx(other, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(other, `select count(*) from open_item_activation_run`).Scan(&visible)
	}); err != nil || visible != 0 {
		t.Fatalf("row level security: %d rows err=%v", visible, err)
	}
}

// The balance is taken as of the activation date across all periods: lines up to and
// including it become one opening item per combination, each later line an item of its
// own, and the open items reconcile to the account balance.
func TestPostgres_ActivationRunSplitsAtTheDateAndReconciles(t *testing.T) {
	e, _ := setup(t)
	if _, _, err := e.store.PutAccount(e.ctx, "STD", "1999", 0, Account{Name: "Clearing", Type: "asset", Postable: true}); err != nil {
		t.Fatal(err)
	}
	feb := time.Date(2026, 2, 20, 0, 0, 0, 0, time.UTC)
	date := testPostingDate
	for _, p := range []struct {
		key, side, amount, dept string
		on                      time.Time
	}{
		{"p1", domain.SideDebit, "100.00", "D100", feb},                  // earlier period
		{"p2", domain.SideCredit, "30.00", "D200", date},                 // on the date: carried in
		{"p3", domain.SideDebit, "40.00", "D100", date.AddDate(0, 0, 2)}, // after: its own item
		{"p4", domain.SideCredit, "5.00", "", date.AddDate(0, 0, 4)},
	} {
		if err := postHistoryOn(e, p.key, p.side, p.amount, p.dept, p.on); err != nil {
			t.Fatalf("post %s: %v", p.key, err)
		}
	}
	run, _, err := e.store.ActivateOpenItemAccount(e.ctx, activationOn(e, "split-1", "1999", date))
	if err != nil {
		t.Fatal(err)
	}
	if run.ItemsCreated != 2 || run.LineItemsCreated != 2 || run.TotalDebit != "140.00" || run.TotalCredit != "35.00" {
		t.Fatalf("run: %+v", run)
	}
	byKind := map[string]string{}
	for _, it := range openItems(t, e, "open") {
		byKind[it.SourceType+"/"+it.Dimensions["DEPT"]+"/"+it.PostingDate] = it.DebitCredit + it.Amount
		if it.SourceType == "gl_line" && it.EntryID == nil {
			t.Fatalf("a later line keeps its entry: %+v", it)
		}
	}
	want := map[string]string{
		"activation/D100/2026-03-10": "D100.00",
		"activation/D200/2026-03-10": "C30.00",
		"gl_line/D100/2026-03-12":    "D40.00",
		"gl_line//2026-03-14":        "C5.00",
	}
	if fmt.Sprint(byKind) != fmt.Sprint(want) {
		t.Fatalf("items: %v", byKind)
	}
	if got, net := openNet(t, e), trialNet(t, e, "1999"); got != net || net != "105.00" {
		t.Fatalf("open items %s must reconcile to the balance %s", got, net)
	}
	var tagged int
	if err := dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `select count(*)::int from open_item where activation_run_id = $1`, run.ID).Scan(&tagged)
	}); err != nil || tagged != 4 {
		t.Fatalf("every item of the run carries its id: %d err=%v", tagged, err)
	}
}

// Lines of an earlier managed spell already have (cleared) items: they are neither carried
// in again nor given a second item, and the open items still reconcile.
func TestPostgres_ActivationRunAfterAnEarlierManagedSpell(t *testing.T) {
	e := oiSetup(t)
	tolerance(t, e, "STD", "0.05", "0")
	later := testPostingDate.AddDate(0, 0, 2)
	postOn(t, e, "s1", domain.SideDebit, "10.00", "A", later)
	postOn(t, e, "s2", domain.SideCredit, "10.00", "A", later)
	if _, _, err := e.store.ClearItems(e.ctx, clearingOf(e, "clr", openItems(t, e, "open"), "STD", false, later)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.store.PutOpenItemAccount(e.ctx, e.ledger.ID, "1999", false); err != nil {
		t.Fatal(err)
	}
	if err := postHistoryOn(e, "s3", domain.SideDebit, "25.00", "", testPostingDate.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	run, _, err := e.store.ActivateOpenItemAccount(e.ctx, activationOn(e, "spell-2", "1999", testPostingDate))
	if err != nil {
		t.Fatal(err)
	}
	if run.ItemsCreated != 0 || run.LineItemsCreated != 1 || run.TotalDebit != "25.00" {
		t.Fatalf("run: %+v", run)
	}
	if got, net := openNet(t, e), trialNet(t, e, "1999"); got != net || net != "25.00" {
		t.Fatalf("open items %s must reconcile to the balance %s", got, net)
	}
}

// Postings racing the run either land before it (and are carried in) or wait for the
// ledger lock and make their own item: none is left as neither.
func TestPostgres_ActivationRunRacingPostings(t *testing.T) {
	e, _ := setup(t)
	if _, _, err := e.store.PutAccount(e.ctx, "STD", "1999", 0, Account{Name: "Clearing", Type: "asset", Postable: true}); err != nil {
		t.Fatal(err)
	}
	postHistory(t, e, "r0", domain.SideDebit, "50.00", "D100")
	var wg sync.WaitGroup
	errs := make(chan error, 9)
	for i := 1; i <= 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- postHistoryOn(e, fmt.Sprintf("r%d", i), domain.SideDebit, "1.00", "D100", testPostingDate)
		}(i)
		if i == 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _, err := e.store.ActivateOpenItemAccount(e.ctx, activation(e, "race", "1999"))
				errs <- err
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got, net := openNet(t, e), trialNet(t, e, "1999"); got != net || net != "58.00" {
		t.Fatalf("open items %s must reconcile to the balance %s", got, net)
	}
}
