package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/projects/internal/store"
)

type fakeGL struct {
	posted     []store.GLPostRequest
	reconciled []store.GLReconcileRequest
}

func (f *fakeGL) PostGL(_ context.Context, _ ledgerclient.Ledger, in store.GLPostRequest) (store.GLPostResult, error) {
	f.posted = append(f.posted, in)
	return store.GLPostResult{Booked: []store.GLBooked{{Kind: "TXN", Ref: "t1", EntryID: ids.New(), Lines: 2}}, Failed: []store.GLFailed{}}, nil
}

func (f *fakeGL) ReconcileGL(_ context.Context, _ ledgerclient.Ledger, in store.GLReconcileRequest) (store.GLReconciliation, error) {
	f.reconciled = append(f.reconciled, in)
	return store.GLReconciliation{Currency: "USD", Reconciled: true, Unposted: store.UnpostedList{Items: []store.UnpostedTxn{}},
		Accounts: []store.GLAccountRecon{{Account: "1410", Control: true, Subledger: decimal.RequireFromString("12.5"), GL: decimal.RequireFromString("12.5")}}}, nil
}

func glRouter(gl *fakeGL) http.Handler {
	r := chi.NewRouter()
	api := New(newFake())
	if gl != nil {
		api.WithGL(gl, ledgerclient.NewFake())
	}
	api.Register(r)
	return r
}

func TestActivateForecast_NeedsPlanDutyIfMatchAndAKnownModel(t *testing.T) {
	h := glRouter(nil)
	path := "/v1/projects/P-1/forecasts/" + ids.New() + "/activate"
	if rec := do(t, h, ctxFor(DutyInquire), http.MethodPost, path, "", map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusForbidden {
		t.Fatalf("without ProjPlan: %d", rec.Code)
	}
	if rec := do(t, h, ctxFor(DutyPlan), http.MethodPost, path, "", nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("without If-Match: %d", rec.Code)
	}
	if rec := do(t, h, ctxFor(DutyPlan), http.MethodPost, path, "", map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown model: %d", rec.Code)
	}
}

func TestGL_PostIsUnavailableWithoutLedger(t *testing.T) {
	entity := ids.New()
	rec := do(t, glRouter(nil), principalCtx(entity, DutyGLPost), http.MethodPost, "/v1/projects/gl-postings", `{"ledgerId":"`+ids.New()+`","currency":"USD"}`, nil)
	if rec.Code != http.StatusServiceUnavailable || errorCode(t, rec) != codeGLUnavailable {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestGL_PostRequiresDutyScopeAndRejectsAmountsInTheBody(t *testing.T) {
	gl := &fakeGL{}
	h := glRouter(gl)
	entity := ids.New()
	ledger := ids.New()
	body := `{"ledgerId":"` + ledger + `","currency":"USD","period":"2026-10"}`

	if rec := do(t, h, principalCtx(entity, DutyInquire), http.MethodPost, "/v1/projects/gl-postings", body, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("without the posting duty: %d", rec.Code)
	}
	if rec := do(t, h, principalCtx(entity, DutyGLPost), http.MethodPost, "/v1/projects/gl-postings", `{"legalEntityId":"`+ids.New()+`","ledgerId":"`+ledger+`","currency":"USD"}`, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("a legal entity outside the caller's scope: %d %s", rec.Code, rec.Body.String())
	}
	for name, bad := range map[string]string{
		"amount":      `{"ledgerId":"` + ledger + `","currency":"USD","amount":"999.00"}`,
		"account":     `{"ledgerId":"` + ledger + `","currency":"USD","account":"9999"}`,
		"ledger":      `{"ledgerId":"x","currency":"USD"}`,
		"currency":    `{"ledgerId":"` + ledger + `","currency":"usd"}`,
		"period":      `{"ledgerId":"` + ledger + `","currency":"USD","period":"2026-13"}`,
		"project id":  `{"ledgerId":"` + ledger + `","currency":"USD","projectId":"bad id"}`,
		"empty entry": `{}`,
	} {
		if rec := do(t, h, principalCtx(entity, DutyGLPost), http.MethodPost, "/v1/projects/gl-postings", bad, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if len(gl.posted) != 0 {
		t.Fatalf("nothing may reach the store on a bad request: %+v", gl.posted)
	}

	rec := do(t, h, principalCtx(entity, DutyGLPost), http.MethodPost, "/v1/projects/gl-postings", body, nil)
	if rec.Code != http.StatusOK || len(gl.posted) != 1 || gl.posted[0] != (store.GLPostRequest{LegalEntityID: entity, LedgerID: ledger, Currency: "USD", Period: "2026-10"}) {
		t.Fatalf("status %d posted %+v body %s", rec.Code, gl.posted, rec.Body.String())
	}
}

func TestGL_ReconciliationFormatsAmountsAndValidates(t *testing.T) {
	gl := &fakeGL{}
	h := glRouter(gl)
	entity := ids.New()
	ledger := ids.New()

	if rec := do(t, h, principalCtx(entity, DutyInquire), http.MethodGet, "/v1/projects/gl-reconciliation?currency=USD", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing ledger: %d", rec.Code)
	}
	if rec := do(t, h, principalCtx(entity, DutyInquire), http.MethodGet, "/v1/projects/gl-reconciliation?ledgerId="+ledger+"&currency=USD&periodTo=2026-1", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad periodTo: %d", rec.Code)
	}
	if rec := do(t, h, principalCtx(entity), http.MethodGet, "/v1/projects/gl-reconciliation?ledgerId="+ledger+"&currency=USD", "", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("without ProjInquire: %d", rec.Code)
	}
	rec := do(t, h, principalCtx(entity, DutyInquire), http.MethodGet, "/v1/projects/gl-reconciliation?ledgerId="+ledger+"&currency=USD&periodTo=2026-10", "", nil)
	var out glReconciliationOut
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d %v %s", rec.Code, err, rec.Body.String())
	}
	if !out.Reconciled || len(out.Accounts) != 1 || !out.Accounts[0].Compared || out.Accounts[0].Subledger != "12.50" || out.Accounts[0].Difference != "0.00" {
		t.Fatalf("%+v", out)
	}
	if len(gl.reconciled) != 1 || gl.reconciled[0].LegalEntityID != entity || gl.reconciled[0].PeriodTo != "2026-10" {
		t.Fatalf("%+v", gl.reconciled)
	}
}
