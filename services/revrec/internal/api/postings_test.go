package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/store"
	"github.com/BenjaminSRussell/passage/services/revrec/migrations"
)

type ledgerRig struct {
	api      *API
	handler  http.Handler
	ledger   *ledgerclient.Fake
	ledgerID string
	clock    *time.Time
	st       *store.Postgres
}

func newLedgerRig(t *testing.T) *ledgerRig {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(app)
	rig := &ledgerRig{ledger: ledgerclient.NewFake(), ledgerID: ids.New(), clock: new(time.Time), st: st}
	*rig.clock = time.Date(2027, 4, 2, 9, 30, 0, 0, time.UTC)
	rig.ledger.AddLedger(rig.ledgerID, entityID)
	rig.api = New(st).WithLedger(rig.ledger, st, map[string]string{entityID: rig.ledgerID})
	rig.api.now = func() time.Time { return *rig.clock }
	r := httpx.NewRouter()
	rig.api.Register(r)
	rig.handler = r
	return rig
}

func (g *ledgerRig) run(t *testing.T, periodEnd string) {
	t.Helper()
	rec := call(t, g.handler, http.MethodPost, "/v1/revrec/recognition-runs", `{"legalEntityId":"`+entityID+`","periodEnd":"`+periodEnd+`"}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("run %s: %d %s", periodEnd, rec.Code, rec.Body)
	}
}

func (g *ledgerRig) post(t *testing.T, want int) []postingOut {
	t.Helper()
	rec := call(t, g.handler, http.MethodPost, "/v1/revrec/journal-postings", `{"legalEntityId":"`+entityID+`"}`, nil)
	if rec.Code != want {
		t.Fatalf("journal-postings status %d, want %d: %s", rec.Code, want, rec.Body)
	}
	var out struct {
		Items []postingOut `json:"items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out.Items
}

func (g *ledgerRig) bill(t *testing.T, deferred, date string) {
	t.Helper()
	n := decimal.RequireFromString(deferred)
	g.billInvoice(t, n.Add(decimal.NewFromInt(7200)), n, date)
}

func (g *ledgerRig) revenueEntries() []ledgerclient.Stored {
	var out []ledgerclient.Stored
	for _, e := range g.ledger.Entries() {
		if e.SourceType == journalSourceType {
			out = append(out, e)
		}
	}
	return out
}

func (g *ledgerRig) reconcile(t *testing.T, period string) glReconciliationOut {
	t.Helper()
	path := "/v1/revrec/gl-reconciliation?legalEntityId=" + entityID + "&currency=USD&period=" + period
	rec := call(t, g.handler, http.MethodGet, path, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("gl-reconciliation status %d: %s", rec.Code, rec.Body)
	}
	var out glReconciliationOut
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestJournalPostings_BookRecognizedRevenueAndReconcileGL2400(t *testing.T) {
	g := newLedgerRig(t)
	g.bill(t, "3600.00", "2027-03-10")
	created := decodeContract(t, call(t, g.handler, http.MethodPost, "/v1/revrec/contracts", bundleContract, map[string]string{idempotencyKey: idemKey}))
	milestone := created.Obligations[1].ID
	if rec := call(t, g.handler, http.MethodPost, "/v1/revrec/milestones/"+milestone+":satisfy", `{"satisfiedOn":"2027-03-25"}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("satisfy: %d %s", rec.Code, rec.Body)
	}
	g.run(t, "2027-03-31")

	if rec := g.reconcile(t, "2027-03"); rec.Reconciled || rec.GLDebit != "0.00" || decimal.RequireFromString(rec.Recognized).IsZero() {
		t.Fatalf("before posting %+v, want recognized revenue the ledger does not hold", rec)
	}

	items := g.post(t, http.StatusOK)
	if len(items) != 1 || items[0].Currency != "USD" || items[0].PostingDate != "2027-03-31" || items[0].JournalEntryID == "" {
		t.Fatalf("postings %+v, want one USD entry dated 2027-03-31", items)
	}
	entries := g.revenueEntries()
	if len(entries) != 1 {
		t.Fatalf("%d ledger entries, want 1", len(entries))
	}
	e := entries[0]
	if e.LedgerID != g.ledgerID || e.LegalEntityID != entityID || e.SourceType != "revenue_recognition" || len(e.Lines) != 2 ||
		e.Lines[0].Account != "2400" || e.Lines[0].Credit || e.Lines[1].Account != "4000" || !e.Lines[1].Credit ||
		!e.Lines[0].Amount.Equal(e.Lines[1].Amount) || fixed(e.Lines[0].Amount, "USD") != items[0].Amount {
		t.Errorf("entry %+v, want Dr 2400 / Cr 4000 for %s", e, items[0].Amount)
	}
	if rec := g.reconcile(t, "2027-03"); !rec.Reconciled || rec.Difference != "0.00" || rec.GLDebit != items[0].Amount || rec.Account != "2400" {
		t.Errorf("after posting %+v, want reconciled at %s", rec, items[0].Amount)
	}

	if again := g.post(t, http.StatusOK); len(again) != 0 || len(g.revenueEntries()) != 1 {
		t.Errorf("second post returned %d items and the ledger holds %d entries, want nothing new", len(again), len(g.revenueEntries()))
	}
	if g.api.journalPostings.Load() != 1 {
		t.Errorf("journal postings counter %d, want 1", g.api.journalPostings.Load())
	}
}

// TestGLReconciliation_ClosingBalanceTiesInvoiceDeferralsToRecognition: the roll-forward ties to GL 2400 month by month, and
// keeps tying through a partial and a full credit note (#29 P3).
func TestGLReconciliation_ClosingBalanceTiesInvoiceDeferralsToRecognition(t *testing.T) {
	g := newLedgerRig(t)
	g.bill(t, "2000.00", "2027-03-10")
	g.bill(t, "1600.00", "2027-03-10")
	created := decodeContract(t, call(t, g.handler, http.MethodPost, "/v1/revrec/contracts", bundleContract, map[string]string{idempotencyKey: idemKey}))
	g.run(t, "2027-03-31")
	g.post(t, http.StatusOK)

	rec := g.reconcile(t, "2027-03")
	recognized, deferred := decimal.RequireFromString(rec.Recognized), decimal.RequireFromString(rec.Deferred)
	if rec.GLCredit != "3600.00" || rec.GLDebit != rec.Recognized || !deferred.Equal(decimal.RequireFromString("3600.00").Sub(recognized)) ||
		rec.GLClosing != rec.Deferred || rec.ClosingDiff != "0.00" || !rec.Reconciled {
		t.Fatalf("March %+v, want credits 3600.00 less debits %s tying to the deferred balance", rec, rec.Recognized)
	}

	*g.clock = time.Date(2027, 5, 2, 9, 0, 0, 0, time.UTC)
	g.run(t, "2027-04-30")
	g.post(t, http.StatusOK)
	april := g.reconcile(t, "2027-04")
	if !april.Reconciled || april.GLCredit != "3600.00" || april.GLClosing != april.Deferred || decimal.RequireFromString(april.Deferred).GreaterThanOrEqual(deferred) {
		t.Fatalf("April %+v, want the closing balance tied and below March's %s", april, rec.Deferred)
	}

	// Then partial and full credit notes: the roll-forward keeps tying to GL 2400 through them (creditnotes_test.go).
	g.creditNotesKeepTheRollForwardTied(t, created)
}

func billedBundle(t *testing.T) (gross, deferred decimal.Decimal) {
	t.Helper()
	hundred := decimal.NewFromInt(100)
	for _, l := range testkit.BundleInvoiceLines {
		net := decimal.RequireFromString(l.UnitPrice).Mul(decimal.RequireFromString(l.Qty)).Mul(hundred.Sub(decimal.RequireFromString(l.DiscountPct))).Div(hundred)
		gross = gross.Add(net)
		if l.Method != "POINT_IN_TIME" {
			deferred = deferred.Add(decimal.RequireFromString(l.Allocated))
		}
	}
	if gross.StringFixed(2) != testkit.BundleInvoiceGross || deferred.StringFixed(2) != testkit.BundleInvoiceDeferred {
		t.Fatalf("bundle invoice bills %s and its allocation defers %s, want %s and %s", gross, deferred, testkit.BundleInvoiceGross, testkit.BundleInvoiceDeferred)
	}
	return gross, deferred
}

func (g *ledgerRig) billInvoice(t *testing.T, gross, deferred decimal.Decimal, date string) {
	t.Helper()
	invoiceID := ids.New()
	if _, err := g.ledger.Post(context.Background(), ledgerclient.Entry{
		LegalEntityID: entityID, LedgerID: g.ledgerID, DocumentID: invoiceID, PostingDate: date, Currency: "USD",
		IdempotencyKey: "billing-invoice-" + invoiceID, SourceType: "billing_invoice",
		Lines: []ledgerclient.Line{
			{Account: "1200", Amount: gross},
			{Account: "4000", Credit: true, Amount: gross.Sub(deferred)},
			{Account: store.DeferredAccount, Credit: true, Amount: deferred},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestGLReconciliation_BundleInvoiceBookedAtAllocationTiesAndEndsTheTermAtZero(t *testing.T) {
	g := newLedgerRig(t)
	gross, deferred := billedBundle(t)
	g.billInvoice(t, gross, deferred, "2027-03-10")
	created := decodeContract(t, call(t, g.handler, http.MethodPost, "/v1/revrec/contracts", bundleContract, map[string]string{idempotencyKey: idemKey}))
	if rec := call(t, g.handler, http.MethodPost, "/v1/revrec/milestones/"+created.Obligations[1].ID+":satisfy", `{"satisfiedOn":"2027-03-25"}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("satisfy: %d %s", rec.Code, rec.Body)
	}
	g.run(t, "2027-03-31")
	g.post(t, http.StatusOK)
	march := g.reconcile(t, "2027-03")
	if !march.Reconciled || march.ClosingDiff != "0.00" || march.GLCredit != "3600.00" ||
		march.Deferred != fixed(deferred.Sub(decimal.RequireFromString(march.Recognized)), "USD") {
		t.Fatalf("March %+v, want the closing balance tied to the allocated 3600.00 less recognition", march)
	}

	*g.clock = time.Date(2028, 4, 2, 9, 0, 0, 0, time.UTC)
	g.run(t, "2028-03-31")
	g.post(t, http.StatusOK)
	last := g.reconcile(t, "2028-03")
	if !last.Reconciled || last.Recognized != "3600.00" || last.Deferred != "0.00" || last.GLClosing != "0.00" || last.ClosingDiff != "0.00" {
		t.Errorf("after the full term %+v, want 3600.00 recognized and 2400 closing at 0.00 on both sides", last)
	}
}

func TestGLReconciliation_InvoiceBilledAboveTheAllocationBreaksTheClosingBalance(t *testing.T) {
	g := newLedgerRig(t)
	g.billInvoice(t, decimal.RequireFromString("10800.00"), decimal.RequireFromString("2800.00"), "2027-03-10")
	call(t, g.handler, http.MethodPost, "/v1/revrec/contracts", bundleContract, map[string]string{idempotencyKey: idemKey})
	g.run(t, "2027-03-31")
	g.post(t, http.StatusOK)

	rec := g.reconcile(t, "2027-03")
	if rec.Difference != "0.00" || rec.ClosingDiff != "800.00" || rec.Reconciled {
		t.Errorf("deferred share 2800.00 against 3600.00 allocated: %+v, want a closing difference of 800.00 and not reconciled", rec)
	}
}

func TestGLReconciliation_MissingInvoiceDeferralBreaksTheClosingBalanceOnly(t *testing.T) {
	g := newLedgerRig(t)
	g.bill(t, "2000.00", "2027-03-10")
	call(t, g.handler, http.MethodPost, "/v1/revrec/contracts", bundleContract, map[string]string{idempotencyKey: idemKey})
	g.run(t, "2027-03-31")
	g.post(t, http.StatusOK)

	rec := g.reconcile(t, "2027-03")
	if rec.Difference != "0.00" || rec.ClosingDiff != "1600.00" || rec.Reconciled {
		t.Errorf("one invoice of 1600.00 not booked: %+v, want debits tied, closing difference 1600.00 and not reconciled", rec)
	}
}

func TestJournalPostings_LedgerFailureRetriesWithoutDoublePosting(t *testing.T) {
	g := newLedgerRig(t)
	g.bill(t, "3600.00", "2027-03-10")
	call(t, g.handler, http.MethodPost, "/v1/revrec/contracts", bundleContract, map[string]string{idempotencyKey: idemKey})
	g.run(t, "2027-03-31")
	g.ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusConflict, Code: ledgerclient.CodePeriodClosed})
	rec := call(t, g.handler, http.MethodPost, "/v1/revrec/journal-postings", `{"legalEntityId":"`+entityID+`"}`, nil)
	if p := decodeProblem(t, rec); rec.Code != http.StatusBadGateway || p.Code != CodeLedgerRefused || len(g.revenueEntries()) != 0 {
		t.Fatalf("failed post %d %s with %d entries, want 502 %s and none", rec.Code, p.Code, len(g.revenueEntries()), CodeLedgerRefused)
	}
	if rec := g.reconcile(t, "2027-03"); rec.Reconciled || rec.Difference == "0.00" {
		t.Errorf("%+v after a refused post, want the unposted revenue as the difference", rec)
	}

	g.ledger.FailPosts(nil)
	*g.clock = time.Date(2027, 5, 2, 9, 0, 0, 0, time.UTC)
	g.run(t, "2027-04-30")
	items := g.post(t, http.StatusOK)
	if len(items) != 2 || items[0].PostingDate != "2027-03-31" || items[1].PostingDate != "2027-04-30" || len(g.revenueEntries()) != 2 {
		t.Fatalf("retry posted %+v with %d entries, want the pending March posting and April", items, len(g.revenueEntries()))
	}
	if rec := g.reconcile(t, "2027-04"); !rec.Reconciled {
		t.Errorf("after retry %+v, want reconciled", rec)
	}
}

func TestJournalPostings_Guards(t *testing.T) {
	g := newLedgerRig(t)
	body := `{"legalEntityId":"` + entityID + `"}`
	if rec := callAs(t, g.handler, http.MethodPost, "/v1/revrec/journal-postings", body, allDuties, ids.New(), nil); rec.Code != http.StatusForbidden {
		t.Errorf("other legal-entity scope %d, want 403", rec.Code)
	}
	if rec := callAs(t, g.handler, http.MethodPost, "/v1/revrec/journal-postings", body, "revrec.view,revrec.maintain", "", nil); rec.Code != http.StatusForbidden {
		t.Errorf("without revrec.post %d, want 403", rec.Code)
	}
	if rec := callAs(t, g.handler, http.MethodGet, "/v1/revrec/gl-reconciliation", "", "revrec.post", "", nil); rec.Code != http.StatusForbidden {
		t.Errorf("reconciliation without revrec.view %d, want 403", rec.Code)
	}
	bad := call(t, g.handler, http.MethodPost, "/v1/revrec/journal-postings", `{"legalEntityId":"x","through":"2099-01-01"}`, nil)
	if p := decodeProblem(t, bad); bad.Code != http.StatusBadRequest || p.Errors["legalEntityId"] == "" || p.Errors["through"] == "" {
		t.Errorf("invalid request %d %+v, want 400 with two field errors", bad.Code, p.Errors)
	}
	badRec := call(t, g.handler, http.MethodGet, "/v1/revrec/gl-reconciliation?legalEntityId=x&currency=usd&period=March", "", nil)
	if p := decodeProblem(t, badRec); badRec.Code != http.StatusBadRequest || p.Errors["legalEntityId"] == "" || p.Errors["currency"] == "" || p.Errors["period"] == "" {
		t.Errorf("invalid reconciliation %d %+v, want 400 with three field errors", badRec.Code, p.Errors)
	}

	unconfigured := newHandler(newFakeStore())
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/revrec/journal-postings", body},
		{http.MethodGet, "/v1/revrec/gl-reconciliation?legalEntityId=" + entityID + "&currency=USD&period=2027-03", ""},
	} {
		rec := call(t, unconfigured, c.method, c.path, c.body, nil)
		if p := decodeProblem(t, rec); rec.Code != http.StatusServiceUnavailable || p.Code != CodeLedgerUnavailable {
			t.Errorf("%s without a ledger %d %s, want 503 %s", c.path, rec.Code, p.Code, CodeLedgerUnavailable)
		}
	}
}

type postResult struct {
	Through     string           `json:"through"`
	Items       []postingOut     `json:"items"`
	Rejected    []rejectedOut    `json:"rejected"`
	OtherLedger []otherLedgerOut `json:"otherLedger"`
}

func (g *ledgerRig) postBody(t *testing.T, body string) postResult {
	t.Helper()
	rec := call(t, g.handler, http.MethodPost, "/v1/revrec/journal-postings", body, nil)
	var out postResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("journal-postings %d %s, want 200", rec.Code, rec.Body)
	}
	return out
}

func TestJournalPostings_PermanentRefusalIsNotReofferedUnlessRetried(t *testing.T) {
	g := newLedgerRig(t)
	call(t, g.handler, http.MethodPost, "/v1/revrec/contracts", bundleContract, map[string]string{idempotencyKey: idemKey})
	g.run(t, "2027-03-31")
	g.ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed})
	body := `{"legalEntityId":"` + entityID + `"}`

	first := g.postBody(t, body)
	if len(first.Items) != 0 || len(first.Rejected) != 1 || first.Rejected[0].LedgerCode != ledgerclient.CodePeriodClosed || first.Rejected[0].PostingDate != "2027-03-31" {
		t.Fatalf("first call %+v, want one rejected March posting", first)
	}
	g.ledger.FailPosts(nil)
	if second := g.postBody(t, body); len(second.Items) != 0 || len(second.Rejected) != 0 || len(g.ledger.Entries()) != 0 {
		t.Fatalf("second call %+v with %d entries, want the rejected lines skipped", second, len(g.ledger.Entries()))
	}

	retried := g.postBody(t, `{"legalEntityId":"`+entityID+`","retryRejected":true}`)
	if len(retried.Items) != 1 || retried.Items[0].PostingDate != "2027-03-31" || len(g.ledger.Entries()) != 1 {
		t.Fatalf("retry %+v with %d entries, want the released March lines posted once", retried, len(g.ledger.Entries()))
	}
}

func TestJournalPostings_ThroughIsClampedToTheLastCompletedMonth(t *testing.T) {
	g := newLedgerRig(t)
	call(t, g.handler, http.MethodPost, "/v1/revrec/contracts", bundleContract, map[string]string{idempotencyKey: idemKey})
	g.run(t, "2027-03-31")
	*g.clock = time.Date(2027, 5, 2, 9, 0, 0, 0, time.UTC)
	g.run(t, "2027-04-30")
	g.run(t, "2027-05-01")

	got := g.postBody(t, `{"legalEntityId":"`+entityID+`","through":"2027-05-02"}`)
	for _, p := range got.Items {
		if p.PostingDate > "2027-04-30" {
			t.Errorf("posted %s with through in the open month, want nothing after 2027-04-30", p.PostingDate)
		}
	}
	if len(got.Items) != 2 {
		t.Errorf("posted %+v, want March and April", got.Items)
	}
	if got.Through != "2027-04-30" {
		t.Errorf("through echoed %q, want the effective 2027-04-30", got.Through)
	}
}

func TestCompletePosting_AcceptedAfterRejectAndRetryMovesTheLinesBackAndSupersedesTheRetry(t *testing.T) {
	g := newLedgerRig(t)
	call(t, g.handler, http.MethodPost, "/v1/revrec/contracts", bundleContract, map[string]string{idempotencyKey: idemKey})
	g.run(t, "2027-03-31")
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: entityID, Duties: testkit.ScopeDuties(entityID)})
	march := time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC)
	postings := g.api.postings

	first, _, err := postings.ClaimPostings(ctx, entityID, g.ledgerID, march, false)
	if err != nil || len(first) != 1 {
		t.Fatalf("claim %v %v, want one posting", first, err)
	}
	if err := postings.RejectPosting(ctx, first[0].ID, ledgerclient.CodePeriodClosed); err != nil {
		t.Fatal(err)
	}
	retry, _, err := postings.ClaimPostings(ctx, entityID, g.ledgerID, march, true)
	if err != nil || len(retry) != 1 || retry[0].ID == first[0].ID {
		t.Fatalf("retry claim %v %v, want a new posting for the released lines", retry, err)
	}

	done, err := postings.CompletePosting(ctx, first[0].ID, "entry-1")
	if err != nil || !done.Reattached || len(done.Superseded) != 1 || done.Superseded[0] != retry[0].ID || len(done.DoublePosted) != 0 {
		t.Fatalf("late complete %+v %v, want the retry superseded", done, err)
	}
	if next, other, err := postings.ClaimPostings(ctx, entityID, g.ledgerID, march, true); err != nil || len(next) != 0 || len(other) != 0 {
		t.Fatalf("claim after supersede %v %v %v, want nothing left to post a second time", next, other, err)
	}

	late, err := postings.CompletePosting(ctx, retry[0].ID, "entry-2")
	if err != nil || len(late.DoublePosted) != 1 || late.DoublePosted[0] != retry[0].ID {
		t.Errorf("a superseded retry that the ledger accepted: %+v %v, want it flagged as double posted", late, err)
	}
}

func TestCompletePosting_FlagsLinesAlreadyAcceptedUnderTheRetry(t *testing.T) {
	g := newLedgerRig(t)
	call(t, g.handler, http.MethodPost, "/v1/revrec/contracts", bundleContract, map[string]string{idempotencyKey: idemKey})
	g.run(t, "2027-03-31")
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: entityID, Duties: testkit.ScopeDuties(entityID)})
	march := time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC)
	postings := g.api.postings

	first, _, _ := postings.ClaimPostings(ctx, entityID, g.ledgerID, march, false)
	if err := postings.RejectPosting(ctx, first[0].ID, ledgerclient.CodePeriodClosed); err != nil {
		t.Fatal(err)
	}
	retry, _, _ := postings.ClaimPostings(ctx, entityID, g.ledgerID, march, true)
	if _, err := postings.CompletePosting(ctx, retry[0].ID, "entry-retry"); err != nil {
		t.Fatal(err)
	}
	done, err := postings.CompletePosting(ctx, first[0].ID, "entry-first")
	if err != nil || len(done.DoublePosted) != 1 || done.DoublePosted[0] != retry[0].ID || len(done.Superseded) != 0 {
		t.Fatalf("late complete %+v %v, want the already accepted retry flagged and no further posting", done, err)
	}
	if next, _, err := postings.ClaimPostings(ctx, entityID, g.ledgerID, march, true); err != nil || len(next) != 0 {
		t.Errorf("claim afterwards %v %v, want nothing to post", next, err)
	}
}

func TestCompletePosting_AfterAConcurrentRejectKeepsTheLedgerEntryAndTheLines(t *testing.T) {
	g := newLedgerRig(t)
	call(t, g.handler, http.MethodPost, "/v1/revrec/contracts", bundleContract, map[string]string{idempotencyKey: idemKey})
	g.run(t, "2027-03-31")
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: entityID, Duties: testkit.ScopeDuties(entityID)})
	march := time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC)

	claimed, _, err := g.api.postings.ClaimPostings(ctx, entityID, g.ledgerID, march, false)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim %v %v, want one posting", claimed, err)
	}
	if err := g.api.postings.RejectPosting(ctx, claimed[0].ID, ledgerclient.CodePeriodClosed); err != nil {
		t.Fatal(err)
	}
	done, err := g.api.postings.CompletePosting(ctx, claimed[0].ID, "entry-1")
	if err != nil || !done.Reattached {
		t.Fatalf("complete after reject %v %v, want reattached", done, err)
	}
	again, other, err := g.api.postings.ClaimPostings(ctx, entityID, g.ledgerID, march, true)
	if err != nil || len(again) != 0 || len(other) != 0 {
		t.Fatalf("claim after reattach %v %v %v, want nothing to post twice", again, other, err)
	}
	if again, err := g.api.postings.CompletePosting(ctx, claimed[0].ID, "entry-1"); err != nil || again.Reattached {
		t.Errorf("second complete %v %v, want a quiet no-op", again, err)
	}
}

func TestJournalPostings_UppercaseLegalEntityMapsToTheLedger(t *testing.T) {
	g := newLedgerRig(t)
	call(t, g.handler, http.MethodPost, "/v1/revrec/contracts", bundleContract, map[string]string{idempotencyKey: idemKey})
	g.run(t, "2027-03-31")
	if got := g.postBody(t, `{"legalEntityId":"`+strings.ToUpper(entityID)+`"}`); len(got.Items) != 1 {
		t.Fatalf("uppercase entity posted %+v, want one posting", got)
	}
	rec := call(t, g.handler, http.MethodGet, "/v1/revrec/gl-reconciliation?legalEntityId="+strings.ToUpper(entityID)+"&currency=USD&period=2027-03", "", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("uppercase reconciliation %d %s, want 200", rec.Code, rec.Body)
	}
}

func TestJournalPostings_PendingOnAnotherLedgerIsReportedNotRetargeted(t *testing.T) {
	g := newLedgerRig(t)
	call(t, g.handler, http.MethodPost, "/v1/revrec/contracts", bundleContract, map[string]string{idempotencyKey: idemKey})
	g.run(t, "2027-03-31")
	g.ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusServiceUnavailable})
	call(t, g.handler, http.MethodPost, "/v1/revrec/journal-postings", `{"legalEntityId":"`+entityID+`"}`, nil)
	g.ledger.FailPosts(nil)

	other := ids.New()
	g.ledger.AddLedger(other, entityID)
	g.api.ledgerIDs = map[string]string{entityID: other}
	got := g.postBody(t, `{"legalEntityId":"`+entityID+`"}`)
	if len(got.Items) != 0 || len(got.OtherLedger) != 1 || got.OtherLedger[0].LedgerID != g.ledgerID || len(g.ledger.Entries()) != 0 {
		t.Fatalf("got %+v with %d entries, want the pending posting listed under otherLedger and nothing posted", got, len(g.ledger.Entries()))
	}
}

func TestJournalPostings_LedgerComesFromConfiguration(t *testing.T) {
	g := newLedgerRig(t)
	call(t, g.handler, http.MethodPost, "/v1/revrec/contracts", bundleContract, map[string]string{idempotencyKey: idemKey})
	g.run(t, "2027-03-31")

	forged := call(t, g.handler, http.MethodPost, "/v1/revrec/journal-postings", `{"legalEntityId":"`+entityID+`","ledgerId":"`+ids.New()+`"}`, nil)
	if forged.Code != http.StatusBadRequest || len(g.ledger.Entries()) != 0 {
		t.Fatalf("request ledgerId %d with %d entries, want 400 and none", forged.Code, len(g.ledger.Entries()))
	}
	g.post(t, http.StatusOK)
	if e := g.ledger.Entries(); len(e) != 1 || e[0].LedgerID != g.ledgerID {
		t.Fatalf("entries %+v, want one on the configured ledger %s", e, g.ledgerID)
	}

	g.api.ledgerIDs = map[string]string{}
	rec := call(t, g.handler, http.MethodPost, "/v1/revrec/journal-postings", `{"legalEntityId":"`+entityID+`"}`, nil)
	if p := decodeProblem(t, rec); rec.Code != http.StatusUnprocessableEntity || p.Code != CodeLedgerNotMapped {
		t.Errorf("unmapped entity %d %s, want 422 %s", rec.Code, p.Code, CodeLedgerNotMapped)
	}

	// The configured ledger is pinned to the entity before anything is posted: another entity's ledger is refused.
	foreign := ids.New()
	g.ledger.AddLedger(foreign, ids.New())
	g.api.ledgerIDs = map[string]string{entityID: foreign}
	*g.clock = time.Date(2027, 5, 2, 9, 0, 0, 0, time.UTC)
	g.run(t, "2027-04-30")
	rec = call(t, g.handler, http.MethodPost, "/v1/revrec/journal-postings", `{"legalEntityId":"`+entityID+`"}`, nil)
	if p := decodeProblem(t, rec); rec.Code != http.StatusUnprocessableEntity || p.Code != CodeLedgerMismatch || len(g.ledger.Entries()) != 1 {
		t.Errorf("ledger of another entity %d %s with %d entries, want 422 %s and nothing posted", rec.Code, p.Code, len(g.ledger.Entries()), CodeLedgerMismatch)
	}
}

func TestParseLedgerIDs(t *testing.T) {
	le, ledger := ids.New(), ids.New()
	got, err := ParseLedgerIDs(le + "=" + ledger + " ,")
	if err != nil || got[le] != ledger || len(got) != 1 {
		t.Errorf("parsed %v %v, want the one pair", got, err)
	}
	upper, err := ParseLedgerIDs(strings.ToUpper(le) + "=" + strings.ToUpper(ledger))
	if err != nil || upper[le] != ledger {
		t.Errorf("uppercase pair parsed %v %v, want lowercase keys and values", upper, err)
	}
	for _, bad := range []string{"x=y", le, le + "=" + ledger + "," + ledger} {
		if _, err := ParseLedgerIDs(bad); err == nil {
			t.Errorf("%q parsed, want an error", bad)
		}
	}
}
