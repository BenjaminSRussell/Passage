package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

// The fake store implements the slice-2 commands with the real domain rules so
// the handler tests check validation, duties, headers and response shapes.

func (f *fakeStore) Adjust(_ context.Context, assetID string, in store.AdjustmentInput, _ string) (store.AdjustResult, error) {
	f.lastAdjust = in
	b, ok := f.books[in.BookID]
	if !ok || b.AssetID != assetID {
		return store.AdjustResult{}, httpx.NewProblem(http.StatusNotFound, store.CodeBookNotFound, "no book")
	}
	impairment := f.impairment[in.BookID]
	adj, err := domain.Adjust(in.Type, domain.BookBalance{Cost: b.Cost, Accumulated: b.AccumulatedDepreciation, Impairment: impairment, Currency: b.Currency}, in.Amount)
	if err != nil {
		return store.AdjustResult{}, err
	}
	b.Cost = b.Cost.Add(adj.CostDelta)
	b.AccumulatedDepreciation = b.AccumulatedDepreciation.Add(adj.AccumulatedDelta)
	b.NBV = b.Cost.Sub(b.AccumulatedDepreciation)
	f.books[in.BookID] = b
	f.impairment[in.BookID] = impairment.Add(adj.ImpairmentDelta)
	return store.AdjustResult{Book: b, Limited: adj.Limited, Requested: in.Amount, Transaction: store.Transaction{ID: ids.New(), AssetBookID: in.BookID,
		Type: string(in.Type), TransactionDate: in.Date, CostAmount: adj.CostDelta, AccumulatedAmount: adj.AccumulatedDelta, ImpairmentAmount: adj.ImpairmentDelta}}, nil
}

func (f *fakeStore) AddAddition(_ context.Context, assetID string, in store.AdditionInput, _ string) (store.Addition, error) {
	f.lastAddition = in
	if _, ok := f.assets[assetID]; !ok {
		return store.Addition{}, httpx.NewProblem(http.StatusNotFound, store.CodeAssetNotFound, "no asset")
	}
	var ccy *string
	if in.Financial {
		ccy = &in.Currency
	}
	return store.Addition{ID: ids.New(), AssetID: assetID, Type: in.Type, Description: in.Description, Quantity: in.Quantity, SerialNumber: in.SerialNumber,
		AddedDate: in.AddedDate, Financial: in.Financial, Cost: in.Cost, Currency: ccy, Transactions: []store.Transaction{}}, nil
}

func (f *fakeStore) ListAdditions(_ context.Context, assetID string) ([]store.Addition, error) {
	if _, ok := f.assets[assetID]; !ok {
		return nil, httpx.NewProblem(http.StatusNotFound, store.CodeAssetNotFound, "no asset")
	}
	return []store.Addition{}, nil
}

func (f *fakeStore) ProposeRun(_ context.Context, in store.RunInput, _ string) (store.DepreciationRun, error) {
	f.lastRun = in
	for _, r := range f.runs {
		if r.LegalEntityID == in.LegalEntityID && r.BookCode == in.BookCode && r.Period == in.Period && r.Status == "Posted" {
			return store.DepreciationRun{}, httpx.NewProblem(http.StatusConflict, domain.CodeAlreadyPosted, "already posted")
		}
	}
	run := store.DepreciationRun{ID: ids.New(), LegalEntityID: in.LegalEntityID, BookCode: in.BookCode, Period: in.Period, Status: "Proposed",
		TotalAmount: decimal.RequireFromString("200"), AssetCount: 1, Version: 1, Lines: []store.RunLine{}}
	f.runs[run.ID] = run
	return run, nil
}

func (f *fakeStore) PostRun(_ context.Context, runID string, version int) (store.DepreciationRun, error) {
	run, ok := f.runs[runID]
	if !ok {
		return store.DepreciationRun{}, httpx.NewProblem(http.StatusNotFound, store.CodeRunNotFound, "no depreciation run")
	}
	if run.Status == "Posted" {
		return store.DepreciationRun{}, httpx.NewProblem(http.StatusConflict, domain.CodeAlreadyPosted, "already posted")
	}
	if version != run.Version {
		return store.DepreciationRun{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
	}
	run.Status, run.Version = "Posted", run.Version+1
	f.runs[runID] = run
	return run, nil
}

func (f *fakeStore) GetRun(_ context.Context, runID string) (store.DepreciationRun, error) {
	run, ok := f.runs[runID]
	if !ok {
		return store.DepreciationRun{}, httpx.NewProblem(http.StatusNotFound, store.CodeRunNotFound, "no depreciation run")
	}
	return run, nil
}

func (f *fakeStore) ListRuns(_ context.Context, _ store.RunFilter) (store.RunPage, error) {
	page := store.RunPage{Items: []store.DepreciationRun{}}
	for _, r := range f.runs {
		r.Lines = nil
		page.Items = append(page.Items, r)
	}
	return page, nil
}

const opsDuties = "assets.asset.view,assets.asset.maintain,assets.asset.depreciate,assets.journal.post"

func adjustBody(bookID, typ, amount string) string {
	return `{"bookId":"` + bookID + `","type":"` + typ + `","transactionDate":"2026-06-30","amount":"` + amount + `"}`
}

func TestAdjust_DutyValidationAndCappedReversal(t *testing.T) {
	f := newFake()
	h := server(f)
	assetID, bookID := addAsset(t, h, midMonthBook)
	path := "/v1/assets/" + assetID + "/transactions"
	if rec := call(h, http.MethodPost, path, adjustBody(bookID, "Impairment", "500"), "assets.asset.maintain", true); rec.Code != http.StatusForbidden {
		t.Fatalf("without assets.journal.post status = %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, path, adjustBody(bookID, "Impairment", "500"), opsDuties, false); rec.Code != http.StatusBadRequest {
		t.Fatalf("without Idempotency-Key status = %d", rec.Code)
	}
	bad := call(h, http.MethodPost, path, `{"bookId":"x","type":"Depreciation","transactionDate":"30/06/2026","amount":"0"}`, opsDuties, true)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad body status = %d", bad.Code)
	}
	for _, field := range []string{"bookId", "type", "transactionDate", "amount"} {
		if problem(t, bad).Errors[field] == "" {
			t.Errorf("field %s not reported: %s", field, bad.Body.String())
		}
	}

	if rec := call(h, http.MethodPost, path, adjustBody(bookID, "Impairment", "500"), opsDuties, true); rec.Code != http.StatusCreated {
		t.Fatalf("impairment status = %d body %s", rec.Code, rec.Body.String())
	}
	if got := f.lastAdjust; got.BookID != bookID || got.Type != domain.AdjustmentImpairment || got.Date.String() != "2026-06-30" || !got.Amount.Equal(decimal.NewFromInt(500)) {
		t.Errorf("store received %+v", got)
	}
	// A reversal of 800 against 500 of recorded impairment is capped (FR-37.9).
	rec := call(h, http.MethodPost, path, adjustBody(bookID, "Impairment", "-800"), opsDuties, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("reversal status = %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Limited     bool     `json:"limited"`
		Warnings    []string `json:"warnings"`
		Transaction struct {
			AccumulatedAmount string `json:"accumulatedAmount"`
		} `json:"transaction"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Limited || out.Transaction.AccumulatedAmount != "-500" || len(out.Warnings) != 1 || out.Warnings[0] != "AST-0512: impairment reversal limited to 500.00" {
		t.Fatalf("reversal response = %s", rec.Body.String())
	}

	// A write-down larger than NBV is a domain rule violation (422).
	neg := call(h, http.MethodPost, path, adjustBody(bookID, "Impairment", "999999"), opsDuties, true)
	if neg.Code != http.StatusUnprocessableEntity || problem(t, neg).Code != domain.CodeInvalidAdjustment {
		t.Fatalf("impairment above NBV status = %d body %s", neg.Code, neg.Body.String())
	}
	if rec := call(h, http.MethodPost, path, adjustBody(ids.New(), "AcquisitionAdj", "10"), opsDuties, true); rec.Code != http.StatusNotFound {
		t.Errorf("unknown book status = %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, "/v1/assets/nope/transactions", adjustBody(bookID, "AcquisitionAdj", "10"), opsDuties, true); rec.Code != http.StatusNotFound {
		t.Errorf("malformed asset id status = %d", rec.Code)
	}
}

func TestAdditions_FinancialRulesAndList(t *testing.T) {
	f := newFake()
	h := server(f)
	a := createAsset(t, h)
	path := "/v1/assets/" + a.ID + "/additions"
	financial := `{"type":"Improvement","description":" Coolant system ","addedDate":"2026-04-01","financial":true,"cost":"2000","currency":"USD","serialNumber":" SN-9 "}`
	if rec := call(h, http.MethodPost, path, financial, "assets.asset.view", true); rec.Code != http.StatusForbidden {
		t.Fatalf("without assets.asset.maintain status = %d", rec.Code)
	}
	rec := call(h, http.MethodPost, path, financial, opsDuties, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if got := f.lastAddition; got.Description != "Coolant system" || *got.SerialNumber != "SN-9" || !got.Cost.Equal(decimal.NewFromInt(2000)) ||
		!got.Quantity.Equal(decimal.NewFromInt(1)) || got.AddedDate.String() != "2026-04-01" {
		t.Errorf("store received %+v", got)
	}

	cases := map[string]struct{ body, field string }{
		"financial without cost":    {`{"type":"Component","description":"Motor","addedDate":"2026-04-01","financial":true,"currency":"USD"}`, "cost"},
		"financial bad currency":    {`{"type":"Component","description":"Motor","addedDate":"2026-04-01","financial":true,"cost":"5","currency":"usd"}`, "currency"},
		"cost beyond minor unit":    {`{"type":"Component","description":"Motor","addedDate":"2026-04-01","financial":true,"cost":"5.001","currency":"USD"}`, "cost"},
		"descriptive with cost":     {`{"type":"Accessory","description":"Cover","addedDate":"2026-04-01","cost":"5"}`, "financial"},
		"unknown type":              {`{"type":"Part","description":"Cover","addedDate":"2026-04-01"}`, "type"},
		"book ids must be uuids":    {`{"type":"Component","description":"Motor","addedDate":"2026-04-01","financial":true,"cost":"5","currency":"USD","bookIds":["x"]}`, "bookIds"},
		"quantity must be positive": {`{"type":"Accessory","description":"Cover","addedDate":"2026-04-01","quantity":"0"}`, "quantity"},
	}
	for name, tc := range cases {
		rec := call(h, http.MethodPost, path, tc.body, opsDuties, true)
		if rec.Code != http.StatusBadRequest || problem(t, rec).Errors[tc.field] == "" {
			t.Errorf("%s: status %d body %s", name, rec.Code, rec.Body.String())
		}
	}
	list := call(h, http.MethodGet, path, "", "assets.asset.view", false)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"items":[]`) {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}
	if rec := call(h, http.MethodGet, "/v1/assets/"+ids.New()+"/additions", "", "assets.asset.view", false); rec.Code != http.StatusNotFound {
		t.Errorf("unknown asset list status = %d", rec.Code)
	}
}

func TestRuns_ProposePostIfMatchAndDuties(t *testing.T) {
	f := newFake()
	h := server(f)
	le := testLE
	body := `{"legalEntityId":"` + le + `","bookCode":"IFRS","period":"2026-03"}`
	if rec := call(h, http.MethodPost, "/v1/assets/depreciation-runs", body, "assets.asset.view,assets.journal.post", true); rec.Code != http.StatusForbidden {
		t.Fatalf("without assets.asset.depreciate status = %d", rec.Code)
	}
	bad := call(h, http.MethodPost, "/v1/assets/depreciation-runs", `{"legalEntityId":"x","bookCode":"ifrs","period":"2026-13"}`, opsDuties, true)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad run status = %d", bad.Code)
	}
	for _, field := range []string{"legalEntityId", "bookCode", "period"} {
		if problem(t, bad).Errors[field] == "" {
			t.Errorf("field %s not reported: %s", field, bad.Body.String())
		}
	}
	scope := do(h, req{method: http.MethodPost, path: "/v1/assets/depreciation-runs", body: body, duties: opsDuties, key: true,
		headers: map[string]string{"X-Passage-Legal-Entity": ids.New()}})
	if scope.Code != http.StatusForbidden {
		t.Fatalf("legal entity outside scope status = %d", scope.Code)
	}

	rec := call(h, http.MethodPost, "/v1/assets/depreciation-runs", body, opsDuties, true)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("propose status = %d etag %q body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if f.lastRun != (store.RunInput{LegalEntityID: le, BookCode: "IFRS", Period: "2026-03"}) {
		t.Errorf("store received %+v", f.lastRun)
	}
	var run store.DepreciationRun
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	post := func(duties, etag string) *httptest.ResponseRecorder {
		headers := map[string]string{}
		if etag != "" {
			headers["If-Match"] = etag
		}
		return do(h, req{method: http.MethodPost, path: "/v1/assets/depreciation-runs/" + run.ID + "/post", duties: duties, headers: headers})
	}
	if rec := post("assets.asset.depreciate", `"1"`); rec.Code != http.StatusForbidden {
		t.Fatalf("post without assets.journal.post status = %d", rec.Code)
	}
	if rec := post(opsDuties, ""); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("post without If-Match status = %d", rec.Code)
	}
	if rec := post(opsDuties, `"7"`); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("post with stale If-Match status = %d", rec.Code)
	}
	if rec := post(opsDuties, `"1"`); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` || !strings.Contains(rec.Body.String(), `"status":"Posted"`) {
		t.Fatalf("post status = %d etag %q body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if rec := post(opsDuties, `"2"`); rec.Code != http.StatusConflict || problem(t, rec).Code != domain.CodeAlreadyPosted {
		t.Fatalf("repost status = %d body %s", rec.Code, rec.Body.String())
	}
	if rec := call(h, http.MethodPost, "/v1/assets/depreciation-runs", body, opsDuties, true); rec.Code != http.StatusConflict {
		t.Fatalf("propose after post status = %d", rec.Code)
	}

	// Reads are open to assets.asset.view or assets.asset.depreciate.
	if rec := call(h, http.MethodGet, "/v1/assets/depreciation-runs/"+run.ID, "", "assets.asset.depreciate", false); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("get run status = %d etag %q", rec.Code, rec.Header().Get("ETag"))
	}
	if rec := call(h, http.MethodGet, "/v1/assets/depreciation-runs", "", "assets.asset.view", false); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), run.ID) {
		t.Fatalf("list runs status = %d body %s", rec.Code, rec.Body.String())
	}
	if rec := call(h, http.MethodGet, "/v1/assets/depreciation-runs", "", "assets.lease.maintain", false); rec.Code != http.StatusForbidden {
		t.Fatalf("list runs without a read duty status = %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/assets/depreciation-runs/nope", "", "assets.asset.view", false); rec.Code != http.StatusNotFound || problem(t, rec).Code != store.CodeRunNotFound {
		t.Fatalf("malformed run id status = %d", rec.Code)
	}
}
