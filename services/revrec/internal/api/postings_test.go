package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
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
	rig.api = New(st).WithLedger(rig.ledger, st)
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
	rec := call(t, g.handler, http.MethodPost, "/v1/revrec/journal-postings", `{"legalEntityId":"`+entityID+`","ledgerId":"`+g.ledgerID+`"}`, nil)
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
	path := "/v1/revrec/gl-reconciliation?legalEntityId=" + entityID + "&ledgerId=" + g.ledgerID + "&currency=USD&period=" + period
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
	rec := call(t, g.handler, http.MethodPost, "/v1/revrec/journal-postings", `{"legalEntityId":"`+entityID+`","ledgerId":"`+g.ledgerID+`"}`, nil)
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
	body := `{"legalEntityId":"` + entityID + `","ledgerId":"` + g.ledgerID + `"}`
	if rec := callAs(t, g.handler, http.MethodPost, "/v1/revrec/journal-postings", body, allDuties, ids.New(), nil); rec.Code != http.StatusForbidden {
		t.Errorf("other legal-entity scope %d, want 403", rec.Code)
	}
	if rec := callAs(t, g.handler, http.MethodPost, "/v1/revrec/journal-postings", body, "revrec.view,revrec.maintain", "", nil); rec.Code != http.StatusForbidden {
		t.Errorf("without revrec.post %d, want 403", rec.Code)
	}
	if rec := callAs(t, g.handler, http.MethodGet, "/v1/revrec/gl-reconciliation", "", "revrec.post", "", nil); rec.Code != http.StatusForbidden {
		t.Errorf("reconciliation without revrec.view %d, want 403", rec.Code)
	}
	bad := call(t, g.handler, http.MethodPost, "/v1/revrec/journal-postings", `{"legalEntityId":"x","ledgerId":"y","through":"2099-01-01"}`, nil)
	if p := decodeProblem(t, bad); bad.Code != http.StatusBadRequest || p.Errors["legalEntityId"] == "" || p.Errors["ledgerId"] == "" || p.Errors["through"] == "" {
		t.Errorf("invalid request %d %+v, want 400 with three field errors", bad.Code, p.Errors)
	}
	badRec := call(t, g.handler, http.MethodGet, "/v1/revrec/gl-reconciliation?legalEntityId=x&currency=usd&period=March", "", nil)
	if p := decodeProblem(t, badRec); badRec.Code != http.StatusBadRequest || p.Errors["legalEntityId"] == "" || p.Errors["ledgerId"] == "" || p.Errors["currency"] == "" || p.Errors["period"] == "" {
		t.Errorf("invalid reconciliation %d %+v, want 400 with four field errors", badRec.Code, p.Errors)
	}

	unconfigured := newHandler(newFakeStore())
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/revrec/journal-postings", body},
		{http.MethodGet, "/v1/revrec/gl-reconciliation?legalEntityId=" + entityID + "&ledgerId=" + g.ledgerID + "&currency=USD&period=2027-03", ""},
	} {
		rec := call(t, unconfigured, c.method, c.path, c.body, nil)
		if p := decodeProblem(t, rec); rec.Code != http.StatusServiceUnavailable || p.Code != CodeLedgerUnavailable {
			t.Errorf("%s without a ledger %d %s, want 503 %s", c.path, rec.Code, p.Code, CodeLedgerUnavailable)
		}
	}
}
