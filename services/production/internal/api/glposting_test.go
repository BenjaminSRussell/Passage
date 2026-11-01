package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

type glBooksStub struct {
	post    store.GLPostRequest
	recon   store.GLReconcileRequest
	posted  store.GLPostResult
	rec     store.WIPReconciliation
	postErr error
	recErr  error
}

func (g *glBooksStub) PostToGL(_ context.Context, _ ledgerclient.Ledger, in store.GLPostRequest) (store.GLPostResult, error) {
	g.post = in
	return g.posted, g.postErr
}

func (g *glBooksStub) ReconcileWIP(_ context.Context, _ ledgerclient.Ledger, in store.GLReconcileRequest) (store.WIPReconciliation, error) {
	g.recon = in
	return g.rec, g.recErr
}

func glServer(books store.GLBooks) http.Handler {
	r := httpx.NewRouter()
	New(&fakeStore{}, Config{}).WithGL(books, ledgerclient.NewFake()).Register(r)
	return r
}

var ledgerID = ids.New()

func TestGLPostings_RunPassesTheRequestAndTheResultThrough(t *testing.T) {
	stub := &glBooksStub{posted: store.GLPostResult{Booked: []store.GLBooked{{Document: "PJ-000001", EntryID: "e1", Lines: 4}},
		Failed: []store.GLFailed{{Document: "PJ-000002", Code: "LEDGER_PERIOD_CLOSED"}}, Remaining: 3}}
	rec := do(glServer(stub), call{method: http.MethodPost, path: "/v1/production/gl-postings:run", duties: []string{DutyJournalPost},
		body: `{"ledgerId":"` + ledgerID + `"}`})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var got glPostView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Remaining != 3 || got.Booked[0].EntryID != "e1" || got.Failed[0].Code != "LEDGER_PERIOD_CLOSED" {
		t.Fatalf("%s %v", rec.Body, err)
	}
	if stub.post.LegalEntityID != entityID || stub.post.LedgerID != ledgerID {
		t.Fatalf("request %+v", stub.post)
	}
}

func TestGLPostings_RunRefusals(t *testing.T) {
	h := glServer(&glBooksStub{})
	post := func(body string, duties ...string) *httptest.ResponseRecorder {
		return do(h, call{method: http.MethodPost, path: "/v1/production/gl-postings:run", duties: duties, body: body})
	}
	expectProblem(t, post(`{"ledgerId":"`+ledgerID+`"}`, DutyRead), http.StatusForbidden, "AUTH-0003")
	expectProblem(t, post(`{}`, DutyJournalPost), http.StatusBadRequest, validationCode)
	expectProblem(t, post(`{"ledgerId":"`+ledgerID+`","legalEntityId":"`+ids.New()+`"}`, DutyJournalPost), http.StatusBadRequest, validationCode)
	expectProblem(t, post(`{"ledgerId":"`+ledgerID+`","orderId":"x"}`, DutyJournalPost), http.StatusBadRequest, validationCode)

	r := httpx.NewRouter()
	New(&fakeStore{}, Config{}).Register(r)
	expectProblem(t, do(r, call{method: http.MethodPost, path: "/v1/production/gl-postings:run", duties: []string{DutyJournalPost}, body: `{"ledgerId":"` + ledgerID + `"}`}),
		http.StatusServiceUnavailable, CodeGLUnavailable)
}

func TestGLReconciliation_FormatsAmountsAndMapsLedgerErrors(t *testing.T) {
	stub := &glBooksStub{rec: store.WIPReconciliation{Currency: "USD", Reconciled: false, Accounts: []store.WIPAccountRecon{
		{Account: "1450", Subledger: decimal.RequireFromString("1951"), Unbooked: decimal.RequireFromString("1951"), GL: decimal.Zero, Difference: decimal.RequireFromString("-1951")}}}}
	h := glServer(stub)
	path := "/v1/production/wip-reconciliation?ledgerId=" + ledgerID + "&currency=USD&periodTo=2026-10"
	rec := do(h, call{method: http.MethodGet, path: path, duties: []string{DutyCostRead}})
	var got glReconcileView
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Reconciled || got.Accounts[0].Subledger != "1951.00" || got.Accounts[0].Difference != "-1951.00" || got.Accounts[0].GL != "0.00" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if stub.recon.PeriodTo != "2026-10" || stub.recon.Currency != "USD" || stub.recon.LegalEntityID != entityID {
		t.Fatalf("request %+v", stub.recon)
	}
	expectProblem(t, do(h, call{method: http.MethodGet, path: path, duties: []string{DutyRead}}), http.StatusForbidden, "AUTH-0003")
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/wip-reconciliation?ledgerId=" + ledgerID + "&currency=usd&periodTo=2026-13", duties: []string{DutyCostRead}}), http.StatusBadRequest, validationCode)

	stub.recErr = &ledgerclient.Error{Op: "balance", Status: http.StatusForbidden, Code: "AUTH-0003"}
	expectProblem(t, do(h, call{method: http.MethodGet, path: path, duties: []string{DutyCostRead}}), http.StatusBadGateway, CodeGLUnavailable)
}
