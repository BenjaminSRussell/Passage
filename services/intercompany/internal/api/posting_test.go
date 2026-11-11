package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/api"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/posting"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/store"
	"github.com/BenjaminSRussell/passage/services/intercompany/migrations"
)

const (
	leA = "00000000-0000-0000-0000-0000000000b1"
	leB = "00000000-0000-0000-0000-0000000000b2"

	postDuties = api.DutyPost + "," + api.DutyView + "," + api.DutyReconcile
)

func postingAPI(t *testing.T) (http.Handler, *ledgerclient.Fake) {
	t.Helper()
	return postingAPIWith(t, func(f *ledgerclient.Fake) ledgerclient.Ledger { return f })
}

// refusingLedger refuses every post to one ledger, as a closed period there would.
type refusingLedger struct {
	ledgerclient.Ledger
	ledger string
}

func (r refusingLedger) Post(ctx context.Context, e ledgerclient.Entry) (string, error) {
	if e.LedgerID == r.ledger {
		return "", &ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed}
	}
	return r.Ledger.Post(ctx, e)
}

func postingAPIWith(t *testing.T, wrap func(*ledgerclient.Fake) ledgerclient.Ledger) (http.Handler, *ledgerclient.Fake) {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool)
	fake := ledgerclient.NewFake()
	fake.AddLedger("L-A", leA)
	fake.AddLedger("L-B", leB)
	a := api.New(st).WithReconciliation(st).WithPosting(posting.New(wrap(fake), st), st, st)
	return router(a), fake
}

func postAs(h http.Handler, method, path, key, body string) *httptest.ResponseRecorder {
	headers := map[string]string{"X-Passage-User": "user-7", tenant.HeaderLegalEntity: leA + "," + leB}
	if key != "" {
		headers["Idempotency-Key"] = key
	}
	return call{method: method, path: path, duties: postDuties, body: body, headers: headers}.on(h)
}

func entityTargets(currencyA, currencyB string) string {
	_ = currencyA
	_ = currencyB
	return fmt.Sprintf(`"entities":[{"entity":"A","legalEntityId":%q,"ledgerId":"L-A"},{"entity":"B","legalEntityId":%q,"ledgerId":"L-B"}]`, leA, leB)
}

const matrixJSON = `"matrix":[{"entityA":"A","entityB":"B","dueFrom":"1500-DUE-FROM-B","dueTo":"2500-DUE-TO-A"}]`

// journalBodyFor is AC-40.4 (USD 1,000.00 expense in A, paid from B's cash) with the posting fields.
func journalBodyFor(counter, rate string) string {
	extra := ""
	if counter != "USD" {
		extra = fmt.Sprintf(`,"rate":%q`, rate)
	}
	return fmt.Sprintf(`{"entity":"A","account":"6100-EXPENSE","side":"debit","amount":"1000.00","currency":"USD","offsetEntity":"B","offsetAccount":"1000-CASH",
		"counterCurrency":%q%s,%s,"postingDate":"2026-11-10",%s}`, counter, extra, matrixJSON, entityTargets("USD", counter))
}

type postedJournal struct {
	ID, PairID, Source, Status, FailureCode, PostingDate string
	Entries                                              []struct {
		Entity, LegalEntityID, LedgerID, Currency, LedgerEntryID, ReversalEntryID string
		Legs                                                                      []struct{ Account, Side, Amount string }
	}
}

func netOf(t *testing.T, f *ledgerclient.Fake, ledger, account string) decimal.Decimal {
	t.Helper()
	b, err := f.Balance(context.Background(), ledgerclient.BalanceQuery{LedgerID: ledger, Account: account})
	if err != nil {
		t.Fatal(err)
	}
	return b.Net()
}

func TestPostJournal_PostsBothSidesAndReplays(t *testing.T) {
	h, fake := postingAPI(t)
	rec := postAs(h, http.MethodPost, "/v1/interco/journals", "j1", journalBodyFor("USD", ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("post = %d %s", rec.Code, rec.Body.String())
	}
	j := decode[postedJournal](t, rec)
	if j.Status != "posted" || len(j.Entries) != 2 || j.Entries[0].LedgerEntryID == "" || j.Entries[1].LedgerEntryID == "" || rec.Header().Get("Location") != "/v1/interco/journals/"+j.ID {
		t.Fatalf("journal = %+v", j)
	}
	if got := netOf(t, fake, "L-A", "6100-EXPENSE"); !got.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("A expense = %s, want 1000", got)
	}
	if got := netOf(t, fake, "L-A", "1500-DUE-FROM-B"); !got.Equal(decimal.NewFromInt(-1000)) {
		t.Errorf("A due-from = %s, want -1000", got)
	}
	if got := netOf(t, fake, "L-B", "2500-DUE-TO-A"); !got.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("B due-to = %s, want 1000", got)
	}
	if len(fake.Entries()) != 2 {
		t.Fatalf("ledger entries = %d, want 2", len(fake.Entries()))
	}

	replay := postAs(h, http.MethodPost, "/v1/interco/journals", "j1", journalBodyFor("USD", ""))
	if replay.Code != http.StatusCreated || replay.Header().Get("Idempotent-Replayed") != "true" || decode[postedJournal](t, replay).ID != j.ID {
		t.Fatalf("replay = %d %s", replay.Code, replay.Body.String())
	}
	if len(fake.Entries()) != 2 {
		t.Errorf("a replay posted again: %d entries", len(fake.Entries()))
	}
	other := strings.Replace(journalBodyFor("USD", ""), `"1000.00"`, `"900.00"`, 1)
	if rec := postAs(h, http.MethodPost, "/v1/interco/journals", "j1", other); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "IC-0841") {
		t.Fatalf("key reuse = %d %s", rec.Code, rec.Body.String())
	}

	got := decode[postedJournal](t, postAs(h, http.MethodGet, "/v1/interco/journals/"+j.ID, "", ""))
	if got.ID != j.ID || got.Status != "posted" {
		t.Fatalf("get = %+v", got)
	}
	list := decode[struct{ Items []postedJournal }](t, postAs(h, http.MethodGet, "/v1/interco/journals?status=posted&source=journal", "", ""))
	if len(list.Items) != 1 {
		t.Fatalf("list = %+v", list)
	}
}

func TestPostJournal_ForeignCurrencyBooksEachSideInItsOwnCurrency(t *testing.T) {
	h, fake := postingAPI(t)
	rec := postAs(h, http.MethodPost, "/v1/interco/journals", "fx1", journalBodyFor("EUR", "0.92"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("post = %d %s", rec.Code, rec.Body.String())
	}
	j := decode[postedJournal](t, rec)
	if j.Entries[0].Currency != "USD" || j.Entries[1].Currency != "EUR" || j.Entries[1].Legs[0].Amount != "920.00" {
		t.Fatalf("entries = %+v", j.Entries)
	}
	if got := netOf(t, fake, "L-B", "1000-CASH"); !got.Equal(decimal.NewFromInt(-920)) {
		t.Errorf("B cash = %s, want -920", got)
	}
}

func TestPostJournal_ASecondSideRefusalReversesTheFirst(t *testing.T) {
	h, fake := postingAPI(t)
	fake.FailPostsIn("EUR", &ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed})
	rec := postAs(h, http.MethodPost, "/v1/interco/journals", "c1", journalBodyFor("EUR", "0.92"))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "IC-0845") {
		t.Fatalf("refused second side = %d %s", rec.Code, rec.Body.String())
	}
	for _, acct := range []string{"6100-EXPENSE", "1500-DUE-FROM-B"} {
		if got := netOf(t, fake, "L-A", acct); !got.IsZero() {
			t.Errorf("A %s = %s after compensation, want 0", acct, got)
		}
	}
	entries := fake.Entries()
	if len(entries) != 2 || entries[1].ReversalOf != entries[0].ID {
		t.Fatalf("ledger entries = %+v", entries)
	}
	list := decode[struct{ Items []postedJournal }](t, postAs(h, http.MethodGet, "/v1/interco/journals?status=compensated", "", ""))
	if len(list.Items) != 1 || list.Items[0].FailureCode != "IC-0845" || list.Items[0].Entries[0].ReversalEntryID == "" {
		t.Fatalf("compensated = %+v", list)
	}
	// The same request again reports the same outcome and posts nothing more.
	again := postAs(h, http.MethodPost, "/v1/interco/journals", "c1", journalBodyFor("EUR", "0.92"))
	if again.Code != http.StatusUnprocessableEntity || len(fake.Entries()) != 2 {
		t.Fatalf("retry = %d, entries %d", again.Code, len(fake.Entries()))
	}
}

func TestPostJournal_AnUnreachableLedgerPausesAndTheSameKeyResumes(t *testing.T) {
	h, fake := postingAPI(t)
	fake.FailPostsIn("EUR", errors.New("connection reset"))
	rec := postAs(h, http.MethodPost, "/v1/interco/journals", "p1", journalBodyFor("EUR", "0.92"))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "IC-0846") {
		t.Fatalf("paused = %d %s", rec.Code, rec.Body.String())
	}
	if len(fake.Entries()) != 1 {
		t.Fatalf("only the first side is posted, got %d entries", len(fake.Entries()))
	}
	fake.FailPostsIn("EUR", nil)
	resumed := postAs(h, http.MethodPost, "/v1/interco/journals", "p1", journalBodyFor("EUR", "0.92"))
	if resumed.Code != http.StatusCreated {
		t.Fatalf("resume = %d %s", resumed.Code, resumed.Body.String())
	}
	if j := decode[postedJournal](t, resumed); j.Status != "posted" || j.Entries[1].LedgerEntryID == "" {
		t.Fatalf("resumed = %+v", j)
	}
	if len(fake.Entries()) != 2 {
		t.Errorf("resuming must not post the first side again: %d entries", len(fake.Entries()))
	}
}

func TestPostJournal_Validation(t *testing.T) {
	h, _ := postingAPI(t)
	if rec := postAs(h, http.MethodPost, "/v1/interco/journals", "", journalBodyFor("USD", "")); rec.Code != http.StatusBadRequest {
		t.Errorf("missing Idempotency-Key = %d", rec.Code)
	}
	noTargets := strings.Replace(journalBodyFor("USD", ""), entityTargets("", ""), `"entities":[]`, 1)
	if rec := postAs(h, http.MethodPost, "/v1/interco/journals", "v1", noTargets); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "entities") {
		t.Errorf("no entities = %d %s", rec.Code, rec.Body.String())
	}
	wrongLedger := strings.Replace(journalBodyFor("USD", ""), `"ledgerId":"L-B"`, `"ledgerId":"L-A"`, 1)
	if rec := postAs(h, http.MethodPost, "/v1/interco/journals", "v2", wrongLedger); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "IC-0847") {
		t.Errorf("a ledger of another entity = %d %s", rec.Code, rec.Body.String())
	}
	noMatrix := strings.Replace(journalBodyFor("USD", ""), matrixJSON, `"matrix":[]`, 1)
	if rec := postAs(h, http.MethodPost, "/v1/interco/journals", "v3", noMatrix); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "IC-0805") {
		t.Errorf("no matrix row = %d %s", rec.Code, rec.Body.String())
	}
	viewOnly := call{method: http.MethodPost, path: "/v1/interco/journals", duties: api.DutyView, body: journalBodyFor("USD", ""), headers: map[string]string{"Idempotency-Key": "d", "X-Passage-User": "u"}}
	if rec := viewOnly.on(h); rec.Code != http.StatusForbidden {
		t.Errorf("view duty cannot post = %d", rec.Code)
	}
}

func TestPostJournal_RefusesASideOutsideTheCallersScope(t *testing.T) {
	h, fake := postingAPI(t)
	onlyA := call{method: http.MethodPost, path: "/v1/interco/journals", duties: api.DutyPost + "," + api.DutyView, body: journalBodyFor("USD", ""),
		headers: map[string]string{"Idempotency-Key": "scope-1", "X-Passage-User": "user-7", tenant.HeaderLegalEntity: leA}}
	if rec := onlyA.on(h); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") {
		t.Fatalf("caller scoped to A posting into B = %d %s", rec.Code, rec.Body.String())
	}
	charge := call{method: http.MethodPost, path: "/v1/interco/charges", duties: api.DutyPost + "," + api.DutyView, body: postChargeBody("scope-src", 1, "Engineer"),
		headers: map[string]string{"Idempotency-Key": "scope-2", "X-Passage-User": "user-7", tenant.HeaderLegalEntity: leA}}
	if rec := charge.on(h); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") {
		t.Fatalf("caller scoped to A charging B = %d %s", rec.Code, rec.Body.String())
	}
	if n := len(fake.Entries()); n != 0 {
		t.Fatalf("ledger entries after refused postings = %d, want 0", n)
	}
}

func TestPostJournal_IsOffWithoutALedger(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	h := router(api.New(store.New(pool)))
	if rec := postAs(h, http.MethodPost, "/v1/interco/journals", "k", journalBodyFor("USD", "")); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no ledger = %d %s", rec.Code, rec.Body.String())
	}
}

// ---- charges (#40-5) ----

func postChargeBody(source string, version int, role string) string {
	return fmt.Sprintf(`{"sourceId":%q,"version":%d,"lendingEntity":"A","borrowingEntity":"B","category":"Hour","resourceRole":%q,"transactionDate":"2026-11-03",
		"quantity":"8","unitCost":"60.00","currency":"USD","costAccount":"6200-PROJECT-COST","recoveryAccount":"4900-COST-RECOVERY","marginAccount":"4950-IC-MARGIN",
		"transferPrices":[{"category":"Hour","resourceRole":"Engineer","method":"CostPlus","markupPct":"10","effectiveFrom":"2026-01-01"}],
		%s,"postingDate":"2026-11-10",%s}`, source, version, role, matrixJSON, entityTargetsAB())
}

func entityTargetsAB() string { return entityTargets("USD", "USD") }

type chargeOut struct {
	ID, SourceID, Status, Cost, Price, Margin, JournalID, FailureCode, TransferPriceMethod string
	Version                                                                                int
}

func TestPostCharge_AC4011PostsCostPlusToBothEntities(t *testing.T) {
	h, fake := postingAPI(t)
	rec := postAs(h, http.MethodPost, "/v1/interco/charges", "", postChargeBody("TS-100", 1, "Engineer"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("charge = %d %s", rec.Code, rec.Body.String())
	}
	c := decode[chargeOut](t, rec)
	if c.Status != "posted" || c.Cost != "480.00" || c.Price != "528.00" || c.Margin != "48.00" || c.JournalID == "" || c.TransferPriceMethod != "CostPlus" {
		t.Fatalf("charge = %+v", c)
	}
	// Borrower B books the price as cost against due-to; lender A books due-from against recovery and margin.
	for ledger, want := range map[string]map[string]string{
		"L-B": {"6200-PROJECT-COST": "528", "2500-DUE-TO-A": "-528"},
		"L-A": {"1500-DUE-FROM-B": "528", "4900-COST-RECOVERY": "-480", "4950-IC-MARGIN": "-48"},
	} {
		for account, v := range want {
			if got := netOf(t, fake, ledger, account); !got.Equal(decimal.RequireFromString(v)) {
				t.Errorf("%s %s = %s, want %s", ledger, account, got, v)
			}
		}
	}
	replay := postAs(h, http.MethodPost, "/v1/interco/charges", "", postChargeBody("TS-100", 1, "Engineer"))
	if replay.Header().Get("Idempotent-Replayed") != "true" || decode[chargeOut](t, replay).ID != c.ID || len(fake.Entries()) != 2 {
		t.Fatalf("replay = %d %s entries %d", replay.Code, replay.Body.String(), len(fake.Entries()))
	}
	if rec := postAs(h, http.MethodPost, "/v1/interco/charges", "", strings.Replace(postChargeBody("TS-100", 1, "Engineer"), `"quantity":"8"`, `"quantity":"9"`, 1)); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "IC-0841") {
		t.Fatalf("same version, other request = %d %s", rec.Code, rec.Body.String())
	}
	got := decode[chargeOut](t, postAs(h, http.MethodGet, "/v1/interco/charges/"+c.ID, "", ""))
	if got.ID != c.ID {
		t.Fatalf("get = %+v", got)
	}
}

func TestPostCharge_NoTransferPriceStaysOnSuspenseUntilRepriced(t *testing.T) {
	h, fake := postingAPI(t)
	// The role has no price row, so BR-40.12 keeps the charge on suspense and posts nothing.
	rec := postAs(h, http.MethodPost, "/v1/interco/charges", "", postChargeBody("TS-200", 1, "Designer"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("suspense = %d %s", rec.Code, rec.Body.String())
	}
	s := decode[chargeOut](t, rec)
	if s.Status != "suspense" || s.FailureCode != "IC-0821" || s.Price != "" || len(fake.Entries()) != 0 {
		t.Fatalf("suspense = %+v entries %d", s, len(fake.Entries()))
	}
	// Version 2 carries a price for the role and posts.
	priced := strings.Replace(postChargeBody("TS-200", 2, "Designer"), `"resourceRole":"Engineer"`, `"resourceRole":"Designer"`, 1)
	rec = postAs(h, http.MethodPost, "/v1/interco/charges", "", priced)
	if rec.Code != http.StatusCreated || decode[chargeOut](t, rec).Status != "posted" || len(fake.Entries()) != 2 {
		t.Fatalf("repriced = %d %s entries %d", rec.Code, rec.Body.String(), len(fake.Entries()))
	}
	list := decode[struct{ Items []chargeOut }](t, postAs(h, http.MethodGet, "/v1/interco/charges?sourceId=TS-200", "", ""))
	if len(list.Items) != 2 || list.Items[0].Version != 2 || list.Items[1].Status != "suspense" {
		t.Fatalf("list = %+v", list)
	}
	if only := decode[struct{ Items []chargeOut }](t, postAs(h, http.MethodGet, "/v1/interco/charges?status=suspense", "", "")); len(only.Items) != 1 {
		t.Fatalf("suspense filter = %+v", only)
	}
}

func TestPostCharge_ARefusedSideCompensatesTheChargeToo(t *testing.T) {
	// The borrower's side posts first; the lender's ledger refuses, so the borrower's entry is reversed.
	h, fake := postingAPIWith(t, func(f *ledgerclient.Fake) ledgerclient.Ledger { return refusingLedger{Ledger: f, ledger: "L-A"} })
	rec := postAs(h, http.MethodPost, "/v1/interco/charges", "", postChargeBody("TS-300", 1, "Engineer"))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "IC-0845") {
		t.Fatalf("refused = %d %s", rec.Code, rec.Body.String())
	}
	for _, acct := range []string{"6200-PROJECT-COST", "2500-DUE-TO-A"} {
		if got := netOf(t, fake, "L-B", acct); !got.IsZero() {
			t.Errorf("B %s = %s after compensation, want 0", acct, got)
		}
	}
	c := decode[struct{ Items []chargeOut }](t, postAs(h, http.MethodGet, "/v1/interco/charges?sourceId=TS-300", "", ""))
	if len(c.Items) != 1 || c.Items[0].Status != "compensated" || c.Items[0].FailureCode != "IC-0845" {
		t.Fatalf("stored = %+v", c)
	}
}
