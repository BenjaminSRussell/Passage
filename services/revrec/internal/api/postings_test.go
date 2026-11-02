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
}

func newLedgerRig(t *testing.T) *ledgerRig {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(app)
	rig := &ledgerRig{ledger: ledgerclient.NewFake(), ledgerID: ids.New(), clock: new(time.Time)}
	*rig.clock = time.Date(2027, 4, 2, 9, 30, 0, 0, time.UTC)
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
	entries := g.ledger.Entries()
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

	if again := g.post(t, http.StatusOK); len(again) != 0 || len(g.ledger.Entries()) != 1 {
		t.Errorf("second post returned %d items and the ledger holds %d entries, want nothing new", len(again), len(g.ledger.Entries()))
	}
	if g.api.journalPostings.Load() != 1 {
		t.Errorf("journal postings counter %d, want 1", g.api.journalPostings.Load())
	}
}

func TestJournalPostings_LedgerFailureRetriesWithoutDoublePosting(t *testing.T) {
	g := newLedgerRig(t)
	call(t, g.handler, http.MethodPost, "/v1/revrec/contracts", bundleContract, map[string]string{idempotencyKey: idemKey})
	g.run(t, "2027-03-31")
	g.ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusConflict, Code: ledgerclient.CodePeriodClosed})
	rec := call(t, g.handler, http.MethodPost, "/v1/revrec/journal-postings", `{"legalEntityId":"`+entityID+`"}`, nil)
	if p := decodeProblem(t, rec); rec.Code != http.StatusBadGateway || p.Code != CodeLedgerRefused || len(g.ledger.Entries()) != 0 {
		t.Fatalf("failed post %d %s with %d entries, want 502 %s and none", rec.Code, p.Code, len(g.ledger.Entries()), CodeLedgerRefused)
	}
	if rec := g.reconcile(t, "2027-03"); rec.Reconciled {
		t.Errorf("reconciled %+v after a refused post, want a difference", rec)
	}

	g.ledger.FailPosts(nil)
	*g.clock = time.Date(2027, 5, 2, 9, 0, 0, 0, time.UTC)
	g.run(t, "2027-04-30")
	items := g.post(t, http.StatusOK)
	if len(items) != 2 || items[0].PostingDate != "2027-03-31" || items[1].PostingDate != "2027-04-30" || len(g.ledger.Entries()) != 2 {
		t.Fatalf("retry posted %+v with %d entries, want the pending March posting and April", items, len(g.ledger.Entries()))
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
	reattached, err := g.api.postings.CompletePosting(ctx, claimed[0].ID, "entry-1")
	if err != nil || !reattached {
		t.Fatalf("complete after reject %v %v, want reattached", reattached, err)
	}
	again, other, err := g.api.postings.ClaimPostings(ctx, entityID, g.ledgerID, march, true)
	if err != nil || len(again) != 0 || len(other) != 0 {
		t.Fatalf("claim after reattach %v %v %v, want nothing to post twice", again, other, err)
	}
	if reattached, err := g.api.postings.CompletePosting(ctx, claimed[0].ID, "entry-1"); err != nil || reattached {
		t.Errorf("second complete %v %v, want a quiet no-op", reattached, err)
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
