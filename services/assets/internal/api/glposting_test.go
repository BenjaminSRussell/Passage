package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/assets/internal/api"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

type glStub struct {
	store.GLBooks
	post   store.GLPostRequest
	saved  store.PostingProfile
	ifm    int
	recErr error
}

func (g *glStub) PostToGL(_ context.Context, _ ledgerclient.Ledger, in store.GLPostRequest) (store.GLPostResult, error) {
	g.post = in
	return store.GLPostResult{Booked: []store.GLBooked{{Document: "run-1", EntryID: "e1", Lines: 2, Transaction: 3}}, Failed: []store.GLFailed{{Document: "tx-2", Code: "LEDGER_PERIOD_CLOSED"}}}, nil
}

func (g *glStub) ReconcileGL(context.Context, ledgerclient.Ledger, store.GLReconcileRequest) (store.GLReconciliation, error) {
	return store.GLReconciliation{Currency: "USD", BookCode: "IFRS", Unposted: 1, Accounts: []store.GLAccountRecon{
		{Account: "1500", Subledger: decimal.RequireFromString("10000"), GL: decimal.Zero, Difference: decimal.RequireFromString("-10000")}}}, g.recErr
}

func (g *glStub) SavePostingProfile(_ context.Context, in store.PostingProfile, ifMatch int, _ string) (store.PostingProfile, bool, error) {
	g.saved, g.ifm = in, ifMatch
	in.Version = ifMatch + 1
	return in, ifMatch == 0, nil
}

func (g *glStub) ListPostingProfiles(context.Context, string) ([]store.PostingProfile, error) {
	return []store.PostingProfile{}, nil
}

func glServer(g store.GLBooks) http.Handler {
	r := httpx.NewRouter()
	api.New(newFake()).WithGL(g, ledgerclient.NewFake()).Register(r)
	return r
}

const glQuery = "?legalEntityId=" + testLE + "&ledgerId=00000000-0000-0000-0000-0000000000b1&bookCode=IFRS&currency=USD"

func TestGL_PostingNeedsTheJournalDutyAndValidInput(t *testing.T) {
	stub := &glStub{}
	h := glServer(stub)
	body := `{"legalEntityId":"` + testLE + `","ledgerId":"` + ids.New() + `","bookCode":"IFRS","currency":"USD"}`
	if rec := call(h, http.MethodPost, "/v1/assets/gl-postings", body, "assets.asset.view", false); rec.Code != http.StatusForbidden {
		t.Fatalf("view duty must not book: %d", rec.Code)
	}
	rec := call(h, http.MethodPost, "/v1/assets/gl-postings", body, "assets.journal.post", false)
	var got store.GLPostResult
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || len(got.Booked) != 1 || got.Failed[0].Code != "LEDGER_PERIOD_CLOSED" || stub.post.BookCode != "IFRS" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for name, bad := range map[string]string{
		"no ledger":         `{"legalEntityId":"` + testLE + `","bookCode":"IFRS","currency":"USD"}`,
		"lower-case book":   `{"legalEntityId":"` + testLE + `","ledgerId":"` + ids.New() + `","bookCode":"ifrs","currency":"USD"}`,
		"bad currency":      `{"legalEntityId":"` + testLE + `","ledgerId":"` + ids.New() + `","bookCode":"IFRS","currency":"US"}`,
		"another legal ent": `{"legalEntityId":"` + ids.New() + `","ledgerId":"` + ids.New() + `","bookCode":"IFRS","currency":"USD"}`,
	} {
		if rec := call(h, http.MethodPost, "/v1/assets/gl-postings", bad, "assets.journal.post", false); rec.Code != http.StatusBadRequest && rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestGL_ReconciliationFormatsAmountsAndMapsLedgerErrors(t *testing.T) {
	stub := &glStub{}
	h := glServer(stub)
	rec := call(h, http.MethodGet, "/v1/assets/gl-reconciliation"+glQuery, "", "assets.asset.view", false)
	var got struct {
		Reconciled bool
		Unposted   int
		Accounts   []struct{ Account, Subledger, GL, Difference string }
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Reconciled || got.Unposted != 1 || got.Accounts[0].Difference != "-10000.00" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(h, http.MethodGet, "/v1/assets/gl-reconciliation?bookCode=IFRS", "", "assets.asset.view", false); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing parameters: %d", rec.Code)
	}
	stub.recErr = &ledgerclient.Error{Op: "balance", Status: http.StatusForbidden, Code: "AUTH-0003"}
	if rec := call(h, http.MethodGet, "/v1/assets/gl-reconciliation"+glQuery, "", "assets.asset.view", false); rec.Code != http.StatusBadGateway || problem(t, rec).Code != api.CodeGLUnavailable {
		t.Fatalf("ledger refusal: %d %s", rec.Code, rec.Body)
	}
}

func TestGL_NotConfiguredAnswers503AndProfilesNeedConfigure(t *testing.T) {
	plain := server(newFake())
	for _, c := range []struct{ method, path, body, duties string }{
		{http.MethodPost, "/v1/assets/gl-postings", `{}`, "assets.journal.post"},
		{http.MethodGet, "/v1/assets/gl-reconciliation" + glQuery, ``, "assets.asset.view"},
		{http.MethodGet, "/v1/assets/posting-profiles?legalEntityId=" + testLE, ``, "assets.asset.view"},
	} {
		if rec := call(plain, c.method, c.path, c.body, c.duties, false); rec.Code != http.StatusServiceUnavailable || problem(t, rec).Code != api.CodeGLUnavailable {
			t.Errorf("%s %s: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
	stub := &glStub{}
	h := glServer(stub)
	profile := `{"legalEntityId":"` + testLE + `","costAccount":"1601","accumulatedDepreciationAccount":"1691","depreciationExpenseAccount":"6901","impairmentAccount":"6911","revaluationAccount":"3401",` +
		`"acquisitionClearingAccount":"2291","proceedsAccount":"1291","gainAccount":"7201","lossAccount":"7211","transferClearingAccount":"1696"}`
	if rec := call(h, http.MethodPut, "/v1/assets/posting-profiles", profile, "assets.asset.view", false); rec.Code != http.StatusForbidden {
		t.Fatalf("profile needs assets.asset.configure: %d", rec.Code)
	}
	rec := call(h, http.MethodPut, "/v1/assets/posting-profiles", profile, "assets.asset.configure", false)
	if rec.Code != http.StatusCreated || stub.saved.ClassCode != "ALL" || stub.ifm != 0 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	rec = do(h, req{method: http.MethodPut, path: "/v1/assets/posting-profiles", body: profile, duties: "assets.asset.configure", headers: map[string]string{"If-Match": `"1"`}})
	if rec.Code != http.StatusOK || stub.ifm != 1 {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body)
	}
	bad := call(h, http.MethodPut, "/v1/assets/posting-profiles", `{"legalEntityId":"`+testLE+`","costAccount":"a b"}`, "assets.asset.configure", false)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid accounts: %d", bad.Code)
	}
}
