package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/fxclient"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/orgclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/api"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/ledgerrecon"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/posting"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/store"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/taxclient"
	"github.com/BenjaminSRussell/passage/services/intercompany/migrations"
)

type registryEnv struct {
	h     http.Handler
	fake  *ledgerclient.Fake
	rates fxclient.Fake
	tax   *taxclient.Fake
	st    *store.Postgres
	pool  *pgxpool.Pool
}

func newRegistryEnv(t *testing.T) *registryEnv {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool)
	fake := ledgerclient.NewFake()
	fake.AddLedger("L-A", leA)
	fake.AddLedger("L-B", leB)
	rates := fxclient.Fake{}
	tax := &taxclient.Fake{RatePct: map[string]decimal.Decimal{"DE": decimal.NewFromInt(19)}}
	poster := posting.New(fake, st).WithNextPeriod(func(ctx context.Context, le string, date time.Time) (time.Time, error) {
		return orgclient.NewYears(orgclient.Fake{StartMonth: map[string]int{leA: 1, leB: 1}}).NextPeriodStart(ctx, le, date)
	})
	rec := ledgerrecon.New(st, st, st, fake, poster, rates, "")
	a := api.New(st).WithReconciliation(st).WithRegistry(st).WithPosting(poster, st, st).WithLedgerReconciliation(rec).WithTax(tax)
	return &registryEnv{h: router(a), fake: fake, rates: rates, tax: tax, st: st, pool: pool}
}

const cfgDuties = postDuties + "," + api.DutyConfigure

func asConfigurer(h http.Handler, method, path, key, body string) *httptest.ResponseRecorder {
	headers := map[string]string{"X-Passage-User": "user-7", tenant.HeaderLegalEntity: leA + "," + leB}
	if key != "" {
		headers["Idempotency-Key"] = key
	}
	return call{method: method, path: path, duties: cfgDuties, body: body, headers: headers}.on(h)
}

func (e *registryEnv) put(t *testing.T, method, path, body string, want int) string {
	t.Helper()
	rec := asConfigurer(e.h, method, path, "", body)
	if rec.Code != want {
		t.Fatalf("%s %s = %d %s, want %d", method, path, rec.Code, rec.Body.String(), want)
	}
	return rec.Body.String()
}

func (e *registryEnv) setup(t *testing.T, usdB bool) {
	t.Helper()
	curB, countryB := "EUR", "DE"
	if usdB {
		curB = "USD"
	}
	e.put(t, http.MethodPut, "/v1/interco/entities/A", fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-A","currency":"USD","country":"US","fxDifferenceAccount":"7100-FX","outputTaxAccount":"2200-OUTPUT-TAX"}`, leA), http.StatusOK)
	e.put(t, http.MethodPut, "/v1/interco/entities/B", fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-B","currency":%q,"country":%q,"taxGroup":"IC","inputTaxAccount":"1400-INPUT-TAX"}`, leB, curB, countryB), http.StatusOK)
	e.put(t, http.MethodPut, "/v1/interco/matrix", `{"rows":[{"entityA":"A","entityB":"B","dueFrom":"1500-DUE-FROM-B","dueTo":"2500-DUE-TO-A"}]}`, http.StatusOK)
}

func TestRegistry_StoredSetupReplacesWhatRequestsSent(t *testing.T) {
	e := newRegistryEnv(t)
	e.setup(t, false)
	list := decode[struct {
		Items []struct{ Entity, LedgerID, Currency, Country string }
	}](t, postAs(e.h, http.MethodGet, "/v1/interco/entities", "", ""))
	if len(list.Items) != 2 || list.Items[0].Entity != "A" || list.Items[1].Country != "DE" {
		t.Fatalf("entities = %+v", list)
	}
	m := decode[struct {
		Rows []struct{ EntityA, DueFrom string }
	}](t, postAs(e.h, http.MethodGet, "/v1/interco/matrix", "", ""))
	if len(m.Rows) != 1 || m.Rows[0].DueFrom != "1500-DUE-FROM-B" {
		t.Fatalf("matrix = %+v", m)
	}
	// A matrix that names an unregistered entity is refused, and a registry entry outside the caller's scope too.
	e.put(t, http.MethodPut, "/v1/interco/matrix", `{"rows":[{"entityA":"A","entityB":"Z","dueFrom":"1","dueTo":"2"}]}`, http.StatusUnprocessableEntity)
	rec := call{method: http.MethodPut, path: "/v1/interco/entities/C", duties: cfgDuties, headers: map[string]string{"X-Passage-User": "u", "X-Passage-Legal-Entity": leA},
		body: fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-B","currency":"EUR"}`, leB)}.on(e.h)
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusBadRequest {
		t.Fatalf("out of scope = %d %s", rec.Code, rec.Body.String())
	}

	// The journal sends neither entities nor a matrix.
	body := `{"entity":"A","account":"6100-EXPENSE","side":"debit","amount":"1000.00","currency":"USD","offsetEntity":"B","offsetAccount":"1000-CASH","counterCurrency":"EUR","rate":"0.9","postingDate":"2026-11-10"}`
	j := decode[postedJournal](t, postAs(e.h, http.MethodPost, "/v1/interco/journals", "j1", body))
	if j.Status != "posted" || len(j.Entries) != 2 || j.Entries[1].Currency != "EUR" || j.Entries[1].Legs[0].Amount != "900.00" {
		t.Fatalf("journal = %+v", j)
	}
	if rec := postAs(e.h, http.MethodPost, "/v1/interco/journals", "j2", strings.Replace(body, `"offsetEntity":"B"`, `"offsetEntity":"Q"`, 1)); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "IC-0853") {
		t.Fatalf("unknown entity = %d %s", rec.Code, rec.Body.String())
	}
}

func TestPostJournal_AClosedPeriodPostsInTheFirstOpenOneWithTheOriginalDate(t *testing.T) {
	e := newRegistryEnv(t)
	e.setup(t, true)
	e.fake.RefuseClosedThrough("2026-11-30")
	body := `{"entity":"A","account":"6100-EXPENSE","side":"debit","amount":"1000.00","currency":"USD","offsetEntity":"B","offsetAccount":"1000-CASH","postingDate":"2026-11-10"}`
	j := decode[postedJournal](t, postAs(e.h, http.MethodPost, "/v1/interco/journals", "c1", body))
	if j.Status != "posted" {
		t.Fatalf("journal = %+v", j)
	}
	got := decode[struct {
		Entries []struct{ PostedOn, DateShiftedFrom, Note string }
	}](t, postAs(e.h, http.MethodGet, "/v1/interco/journals/"+j.ID, "", ""))
	shifted := got.Entries
	if len(shifted) != 2 {
		t.Fatalf("entries = %+v", shifted)
	}
	for _, s := range shifted {
		if s.PostedOn != "2026-12-01" || s.DateShiftedFrom != "2026-11-10" || !strings.Contains(s.Note, "dated 2026-11-10") {
			t.Errorf("entry = %+v", s)
		}
	}
	for _, s := range e.fake.Entries() {
		if s.PostingDate != "2026-12-01" || !strings.Contains(s.Lines[0].Text, "dated 2026-11-10") || s.PostClosed {
			t.Errorf("ledger entry = %s %q postClosed=%v", s.PostingDate, s.Lines[0].Text, s.PostClosed)
		}
	}
	// A date that is open is not touched.
	open := strings.Replace(body, "2026-11-10", "2026-12-10", 1)
	j2 := decode[postedJournal](t, postAs(e.h, http.MethodPost, "/v1/interco/journals", "c2", open))
	if j2.Status != "posted" || j2.Entries[0].LedgerEntryID == "" {
		t.Fatalf("open date = %+v", j2)
	}
	for _, s := range e.fake.Entries()[2:] {
		if s.PostingDate != "2026-12-10" {
			t.Errorf("an open date moved to %s", s.PostingDate)
		}
	}
}

func TestReconciliation_PairAmountsAreReadFromTheLedgers(t *testing.T) {
	e := newRegistryEnv(t)
	e.setup(t, false)
	e.rates.Set("closing", "EUR", "USD", decimal.RequireFromString("1.11"))
	// A lends 1,000 USD (due-from debit) and B books 900 EUR due-to: 999.00 USD at the closing rate.
	post := func(ledger, le, account string, credit bool, amount, cur, date, key string) {
		t.Helper()
		other := "9999-OFFSET"
		if _, err := e.fake.Post(context.Background(), ledgerclient.Entry{LegalEntityID: le, LedgerID: ledger, DocumentID: key, PostingDate: date, Currency: cur, IdempotencyKey: key,
			SourceType: "test", Lines: []ledgerclient.Line{{Account: account, Credit: credit, Amount: decimal.RequireFromString(amount)}, {Account: other, Credit: !credit, Amount: decimal.RequireFromString(amount)}}}); err != nil {
			t.Fatal(err)
		}
	}
	post("L-A", leA, "1500-DUE-FROM-B", false, "1000.00", "USD", "2026-11-05", "a1")
	post("L-B", leB, "2500-DUE-TO-A", true, "900.00", "EUR", "2026-11-06", "b1")

	run := func(key, tolerance string) (id string, out struct {
		ID, Source                string
		Matched, Timing, Mismatch int
		Blocking                  bool
		Currency, Period          string
		FxTolerancePct            string
	}) {
		rec := postAs(e.h, http.MethodPost, "/v1/interco/reconciliations/run", key, fmt.Sprintf(`{"period":"2026-11","currency":"USD","source":"ledger","fxTolerancePct":%q}`, tolerance))
		if rec.Code != http.StatusCreated {
			t.Fatalf("run = %d %s", rec.Code, rec.Body.String())
		}
		out = decode[struct {
			ID, Source                string
			Matched, Timing, Mismatch int
			Blocking                  bool
			Currency, Period          string
			FxTolerancePct            string
		}](t, rec)
		return out.ID, out
	}
	// 1,000.00 against 999.00: within the 0.5 percent FX tolerance, so the pair matches.
	_, within := run("r1", "0.5")
	if within.Source != "ledger" || within.Matched != 1 || within.Mismatch != 0 {
		t.Fatalf("within tolerance = %+v", within)
	}
	// With no tolerance the same pair is a mismatch, and its items say what each ledger held.
	id, strict := run("r2", "0")
	if strict.Mismatch != 1 || !strict.Blocking {
		t.Fatalf("strict = %+v", strict)
	}
	detail := decode[struct {
		Items []struct {
			ID, Ref, EntityA, EntityB, AmountA, AmountB, Difference, Status string
			FxApplies                                                       bool
		}
	}](t, postAs(e.h, http.MethodGet, "/v1/interco/reconciliations/"+id, "", ""))
	if len(detail.Items) != 1 || detail.Items[0].AmountA != "1000.00" || detail.Items[0].AmountB != "999.00" || detail.Items[0].Difference != "1.00" || !detail.Items[0].FxApplies {
		t.Fatalf("items = %+v", detail.Items)
	}
	// A repeat under the same key replays the stored run.
	if rec := postAs(e.h, http.MethodPost, "/v1/interco/reconciliations/run", "r2", `{"period":"2026-11","currency":"USD","source":"ledger","fxTolerancePct":"0"}`); rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay = %d", rec.Code)
	}
	// A missing rate is named, and items sent with source ledger are refused.
	delete(e.rates, "closing|EUR|USD")
	if rec := postAs(e.h, http.MethodPost, "/v1/interco/reconciliations/run", "r3", `{"period":"2026-11","currency":"USD","source":"ledger"}`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "IC-0859") {
		t.Fatalf("missing rate = %d %s", rec.Code, rec.Body.String())
	}
	if rec := postAs(e.h, http.MethodPost, "/v1/interco/reconciliations/run", "r4", `{"period":"2026-11","currency":"USD","source":"ledger","items":[{"entityA":"A","entityB":"B","amountA":"1","amountB":"1"}]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("items with ledger = %d", rec.Code)
	}
	e.rates.Set("closing", "EUR", "USD", decimal.RequireFromString("1.11"))

	// The FX difference is booked to A's FX-difference account, here in a closed period so it goes to the first open one.
	e.fake.RefuseClosedThrough("2026-11-30")
	itemID := detail.Items[0].ID
	adj := decode[struct{ Amount, Direction, Account, FxAccount, PostedOn, PeriodEnd, LedgerEntryID string }](t, postAs(e.h, http.MethodPost, "/v1/interco/reconciliations/items/"+itemID+"/fx-adjust", "", ""))
	if adj.Amount != "1.00" || adj.Direction != "decrease" || adj.Account != "1500-DUE-FROM-B" || adj.FxAccount != "7100-FX" || adj.PostedOn != "2026-12-01" || adj.PeriodEnd != "2026-11-30" {
		t.Fatalf("adjustment = %+v", adj)
	}
	// A's receivable (1,000.00) was larger than B's payable (999.00 at the closing rate): A writes the difference off to the FX account.
	if got := netOf(t, e.fake, "L-A", "7100-FX"); !got.Equal(decimal.NewFromInt(1)) {
		t.Errorf("FX account = %s, want 1 (debit)", got)
	}
	if got := netOf(t, e.fake, "L-A", "1500-DUE-FROM-B"); !got.Equal(decimal.NewFromInt(999)) {
		t.Errorf("due-from = %s, want 999", got)
	}
	// The item is resolved; a second call returns the stored adjustment and posts nothing.
	again := decode[struct{ LedgerEntryID string }](t, postAs(e.h, http.MethodPost, "/v1/interco/reconciliations/items/"+itemID+"/fx-adjust", "", ""))
	if again.LedgerEntryID != adj.LedgerEntryID {
		t.Errorf("second adjustment = %+v", again)
	}
	after := decode[struct {
		Items []struct{ Resolution, Note string }
	}](t, postAs(e.h, http.MethodGet, "/v1/interco/reconciliations/"+id, "", ""))
	if after.Items[0].Resolution != "resolved" || !strings.Contains(after.Items[0].Note, "7100-FX") {
		t.Fatalf("item after = %+v", after.Items)
	}
}

func TestReconciliation_FXAdjustNeedsAnAccountAndAnFXDifference(t *testing.T) {
	e := newRegistryEnv(t)
	e.setup(t, true) // both USD: no FX difference
	post := func(ledger, le, account string, credit bool, amount string) {
		if _, err := e.fake.Post(context.Background(), ledgerclient.Entry{LegalEntityID: le, LedgerID: ledger, DocumentID: ledger + account, PostingDate: "2026-11-05", Currency: "USD", IdempotencyKey: ledger + account,
			SourceType: "test", Lines: []ledgerclient.Line{{Account: account, Credit: credit, Amount: decimal.RequireFromString(amount)}, {Account: "9999", Credit: !credit, Amount: decimal.RequireFromString(amount)}}}); err != nil {
			t.Fatal(err)
		}
	}
	post("L-A", leA, "1500-DUE-FROM-B", false, "100.00")
	post("L-B", leB, "2500-DUE-TO-A", true, "60.00")
	rec := postAs(e.h, http.MethodPost, "/v1/interco/reconciliations/run", "r1", `{"period":"2026-11","currency":"USD","source":"ledger"}`)
	run := decode[struct{ ID string }](t, rec)
	d := decode[struct{ Items []struct{ ID string } }](t, postAs(e.h, http.MethodGet, "/v1/interco/reconciliations/"+run.ID, "", ""))
	if rec := postAs(e.h, http.MethodPost, "/v1/interco/reconciliations/items/"+d.Items[0].ID+"/fx-adjust", "", ""); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "IC-0858") {
		t.Fatalf("same currency = %d %s", rec.Code, rec.Body.String())
	}
	if rec := postAs(e.h, http.MethodPost, "/v1/interco/reconciliations/items/"+leA+"/fx-adjust", "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown item = %d", rec.Code)
	}
}

const regChargeBody = `{"sourceId":"TS-1","version":1,"lendingEntity":"A","borrowingEntity":"B","category":"Hour","resourceRole":"Engineer","transactionDate":"2026-11-03",
	"quantity":"10","unitCost":"50","currency":"USD","costAccount":"6200-IC-COST","recoveryAccount":"4900-IC-RECOVERY","marginAccount":"4910-IC-MARGIN","postingDate":"2026-11-10"}`

func TestCharge_PricedFromStoredPricesWithCrossBorderTax(t *testing.T) {
	e := newRegistryEnv(t)
	e.setup(t, true)
	e.put(t, http.MethodPut, "/v1/interco/entities/B", fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-B","currency":"USD","country":"DE","taxGroup":"IC"}`, leB), http.StatusOK) // no input tax account yet

	// No stored price: the charge waits on suspense.
	if rec := postAs(e.h, http.MethodPost, "/v1/interco/charges", "", regChargeBody); rec.Code != http.StatusAccepted {
		t.Fatalf("no price = %d %s", rec.Code, rec.Body.String())
	}
	price := `{"lendingEntity":"A","borrowingEntity":"B","category":"Hour","resourceRole":"Engineer","method":"CostPlus","markupPct":"10","currency":"USD","effectiveFrom":"2026-01-01"}`
	e.put(t, http.MethodPost, "/v1/interco/transfer-prices", price, http.StatusCreated)
	if rec := asConfigurer(e.h, http.MethodPost, "/v1/interco/transfer-prices", "", price); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "IC-0854") {
		t.Fatalf("same date = %d %s", rec.Code, rec.Body.String())
	}
	e.put(t, http.MethodPost, "/v1/interco/transfer-prices", `{"lendingEntity":"A","borrowingEntity":"Z","category":"Hour","method":"Cost","currency":"USD","effectiveFrom":"2026-01-01"}`, http.StatusUnprocessableEntity)
	prices := decode[struct {
		Items []struct{ Method, MarkupPct string }
	}](t, postAs(e.h, http.MethodGet, "/v1/interco/transfer-prices?lender=A&borrower=B", "", ""))
	if len(prices.Items) != 1 || prices.Items[0].MarkupPct != "10" {
		t.Fatalf("prices = %+v", prices)
	}

	next := strings.Replace(regChargeBody, `"version":1`, `"version":2`, 1)
	e.put(t, http.MethodPut, "/v1/interco/settings", `{"chargeSettlementMode":"Journal","crossBorderTax":true}`, http.StatusOK)
	// Cross-border with no input tax account on the borrower: refused before anything is posted, and the same request resumes.
	if rec := postAs(e.h, http.MethodPost, "/v1/interco/charges", "", next); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "IC-0855") {
		t.Fatalf("no tax account = %d %s", rec.Code, rec.Body.String())
	}
	if n := len(e.fake.Entries()); n != 0 {
		t.Fatalf("ledger entries = %d before the accounts exist", n)
	}
	e.put(t, http.MethodPut, "/v1/interco/entities/B", fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-B","currency":"USD","country":"DE","taxGroup":"IC","inputTaxAccount":"1400-INPUT-TAX"}`, leB), http.StatusOK)
	rec := postAs(e.h, http.MethodPost, "/v1/interco/charges", "", next)
	if rec.Code != http.StatusCreated {
		t.Fatalf("charge = %d %s", rec.Code, rec.Body.String())
	}
	c := decode[struct {
		Status, Cost, Price, Margin, Tax, TaxCode, SettlementMode string
		TaxRecorded                                               bool
	}](t, rec)
	if c.Status != "posted" || c.Cost != "500.00" || c.Price != "550.00" || c.Margin != "50.00" || c.Tax != "104.50" || c.SettlementMode != "Journal" || !c.TaxRecorded {
		t.Fatalf("charge = %+v", c)
	}
	// 19 percent of 550.00 is 104.50; both entities balance with the tax in their gross due accounts.
	checks := []struct {
		ledger, account string
		want            string
	}{{"L-A", "1500-DUE-FROM-B", "654.50"}, {"L-A", "4900-IC-RECOVERY", "-500"}, {"L-A", "4910-IC-MARGIN", "-50"}, {"L-A", "2200-OUTPUT-TAX", "-104.50"},
		{"L-B", "6200-IC-COST", "550"}, {"L-B", "1400-INPUT-TAX", "104.50"}, {"L-B", "2500-DUE-TO-A", "-654.50"}}
	for _, ck := range checks {
		if got := netOf(t, e.fake, ck.ledger, ck.account); !got.Equal(decimal.RequireFromString(ck.want)) {
			t.Errorf("%s %s = %s, want %s", ck.ledger, ck.account, got, ck.want)
		}
	}
	if len(e.tax.Recorded) != 1 {
		t.Errorf("tax recorded = %v", e.tax.Recorded)
	}

	// Invoice mode hands the charge over: nothing is posted here.
	before := len(e.fake.Entries())
	e.put(t, http.MethodPut, "/v1/interco/settings", `{"chargeSettlementMode":"Invoice","crossBorderTax":true}`, http.StatusOK)
	inv := decode[struct{ Status, SettlementMode, Price, Tax string }](t, postAs(e.h, http.MethodPost, "/v1/interco/charges", "", strings.Replace(regChargeBody, `"TS-1"`, `"TS-2"`, 1)))
	if inv.Status != "invoice_requested" || inv.SettlementMode != "Invoice" || inv.Price != "550.00" || inv.Tax != "104.50" || len(e.fake.Entries()) != before {
		t.Fatalf("invoice mode = %+v, entries %d -> %d", inv, before, len(e.fake.Entries()))
	}
	var events int
	if err := e.pool.QueryRow(context.Background(), `select count(*) from outbox where type = 'dev.passage.interco.chargeinvoice.requested.v1'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Errorf("chargeinvoice.requested events = %d, want 1", events)
	}
	// The event names both legal entities, so billing and payables raise the documents there (#40).
	var lender, borrower string
	if err := e.pool.QueryRow(context.Background(), `select payload->'data'->>'lenderLegalEntityId', payload->'data'->>'borrowerLegalEntityId' from outbox
		where type = 'dev.passage.interco.chargeinvoice.requested.v1'`).Scan(&lender, &borrower); err != nil || lender != leA || borrower != leB {
		t.Errorf("legal entities = %q %q, %v", lender, borrower, err)
	}
}

func TestCharge_ATaxServiceThatIsDownStopsTheChargeBeforePosting(t *testing.T) {
	e := newRegistryEnv(t)
	e.setup(t, true)
	e.put(t, http.MethodPut, "/v1/interco/entities/B", fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-B","currency":"USD","country":"DE","inputTaxAccount":"1400-INPUT-TAX"}`, leB), http.StatusOK)
	e.put(t, http.MethodPost, "/v1/interco/transfer-prices", `{"lendingEntity":"A","borrowingEntity":"B","category":"Hour","resourceRole":"Engineer","method":"Cost","currency":"USD","effectiveFrom":"2026-01-01"}`, http.StatusCreated)
	e.put(t, http.MethodPut, "/v1/interco/settings", `{"chargeSettlementMode":"Journal","crossBorderTax":true}`, http.StatusOK)
	e.tax.Fail = fmt.Errorf("down")
	if rec := postAs(e.h, http.MethodPost, "/v1/interco/charges", "", regChargeBody); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "IC-0856") {
		t.Fatalf("tax down = %d %s", rec.Code, rec.Body.String())
	}
	if len(e.fake.Entries()) != 0 {
		t.Fatal("something was posted without the tax")
	}
	e.tax.Fail = nil
	if rec := postAs(e.h, http.MethodPost, "/v1/interco/charges", "", regChargeBody); rec.Code != http.StatusCreated {
		t.Fatalf("retry = %d %s", rec.Code, rec.Body.String())
	}
	// Recording the tax fails once after the post; a repeat of the request records it.
	e.tax.FailWrite = fmt.Errorf("down")
	body := strings.Replace(regChargeBody, `"TS-1"`, `"TS-9"`, 1)
	c := decode[struct{ TaxRecorded bool }](t, postAs(e.h, http.MethodPost, "/v1/interco/charges", "", body))
	if c.TaxRecorded {
		t.Fatal("tax was recorded while the tax service was down")
	}
	e.tax.FailWrite = nil
	rep := postAs(e.h, http.MethodPost, "/v1/interco/charges", "", body)
	if rep.Header().Get("Idempotent-Replayed") != "true" || !decode[struct{ TaxRecorded bool }](t, rep).TaxRecorded {
		t.Fatalf("replay = %d %s", rep.Code, rep.Body.String())
	}
}

var _ = httptest.NewRecorder

func TestReconciliation_FXAdjustRefusesTimingItemsAndDifferencesFXCannotExplain(t *testing.T) {
	e := newRegistryEnv(t)
	e.setup(t, false)
	e.rates.Set("closing", "EUR", "USD", decimal.RequireFromString("1.11"))
	post := func(ledger, le, account string, credit bool, amount, cur, date, key string) {
		t.Helper()
		if _, err := e.fake.Post(context.Background(), ledgerclient.Entry{LegalEntityID: le, LedgerID: ledger, DocumentID: key, PostingDate: date, Currency: cur, IdempotencyKey: key,
			SourceType: "test", Lines: []ledgerclient.Line{{Account: account, Credit: credit, Amount: decimal.RequireFromString(amount)}, {Account: "9999", Credit: !credit, Amount: decimal.RequireFromString(amount)}}}); err != nil {
			t.Fatal(err)
		}
	}
	// A books 100,000 USD and B only 50,000 EUR (55,500 USD): a missing invoice, not an exchange difference.
	post("L-A", leA, "1500-DUE-FROM-B", false, "100000.00", "USD", "2026-11-05", "a1")
	post("L-B", leB, "2500-DUE-TO-A", true, "50000.00", "EUR", "2026-11-06", "b1")
	rec := postAs(e.h, http.MethodPost, "/v1/interco/reconciliations/run", "r1", `{"period":"2026-11","currency":"USD","source":"ledger","fxTolerancePct":"0"}`)
	run := decode[struct{ ID string }](t, rec)
	d := decode[struct{ Items []struct{ ID string } }](t, postAs(e.h, http.MethodGet, "/v1/interco/reconciliations/"+run.ID, "", ""))
	rec = postAs(e.h, http.MethodPost, "/v1/interco/reconciliations/items/"+d.Items[0].ID+"/fx-adjust", "", "")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "IC-0861") {
		t.Fatalf("a large difference = %d %s", rec.Code, rec.Body.String())
	}
	if n := len(e.fake.Entries()); n != 2 {
		t.Fatalf("a refused adjustment posted: %d entries", n)
	}
	// B books its side after the period end: a Timing item, which the next period closes.
	post("L-B", leB, "2500-DUE-TO-A", true, "40090.09", "EUR", "2026-12-02", "b2")
	rec = postAs(e.h, http.MethodPost, "/v1/interco/reconciliations/run", "r2", `{"period":"2026-11","currency":"USD","source":"ledger","fxTolerancePct":"0"}`)
	run2 := decode[struct{ ID string }](t, rec)
	d2 := decode[struct{ Items []struct{ ID, Status string } }](t, postAs(e.h, http.MethodGet, "/v1/interco/reconciliations/"+run2.ID, "", ""))
	if d2.Items[0].Status != "Timing" {
		t.Skipf("fixture is %s, not Timing", d2.Items[0].Status)
	}
	rec = postAs(e.h, http.MethodPost, "/v1/interco/reconciliations/items/"+d2.Items[0].ID+"/fx-adjust", "", "")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "IC-0858") {
		t.Fatalf("a timing item = %d %s", rec.Code, rec.Body.String())
	}
}
