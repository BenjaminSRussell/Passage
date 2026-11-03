package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/costing/internal/store"
)

type slice7 struct {
	profile   store.PostingProfile
	glRuns    []store.GLPostRequest
	closed    []store.CloseRequest
	tieOutFor string
	closeErr  error
	replayed  bool
}

func (f *slice7) GetPostingProfile(context.Context, string) (store.PostingProfile, error) {
	return f.profile, nil
}

func (f *slice7) SavePostingProfile(_ context.Context, in store.PostingProfile, _ string) (store.PostingProfile, error) {
	f.profile = in
	return in, nil
}

func (f *slice7) PostCostVouchers(_ context.Context, _ ledgerclient.Ledger, in store.GLPostRequest) (store.GLPostResult, error) {
	f.glRuns = append(f.glRuns, in)
	return store.GLPostResult{Booked: []store.GLBooked{{CostTransactionID: ids.New(), EntryID: ids.New(), Lines: 3}}, Failed: []store.GLFailed{}}, nil
}

func (f *slice7) RunClose(_ context.Context, _ ledgerclient.Ledger, in store.CloseRequest, _ string) (store.Close, error) {
	f.closed = append(f.closed, in)
	if f.closeErr != nil {
		return store.Close{}, f.closeErr
	}
	return store.Close{ID: ids.New(), LegalEntityID: in.LegalEntityID, PeriodEnd: in.PeriodEnd, Status: "COMPLETED", Replayed: f.replayed}, nil
}

func (f *slice7) ListCloses(context.Context, string) ([]store.Close, error) { return nil, nil }

func (f *slice7) ReconcileInventory(_ context.Context, _ ledgerclient.Ledger, le, asOf string) (store.InventoryReconciliation, error) {
	f.tieOutFor = le + asOf
	return store.InventoryReconciliation{LegalEntityID: le, AsOf: asOf, Currency: "EUR", Account: "1400", ParkedReasons: map[string]int{"LEDGER_PERIOD_CLOSED": 1}, Parked: 1}, nil
}

func ledgerServer() (http.Handler, *fakeStore, *ledgerclient.Fake) {
	fs := &fakeStore{versions: map[string]store.CostingVersion{}}
	fake := ledgerclient.NewFake()
	r := httpx.NewRouter()
	New(fs).WithLedger(fake).Register(r)
	return r, fs, fake
}

const glDuties = allDuties + "," + dutyPostingProfileWrite + "," + dutyGLPost + "," + dutyCloseRun

func profileBody(le, ledger string) string {
	return `{"legalEntityId":"` + le + `","ledgerId":"` + ledger + `","inventoryAccount":"1400","grirAccount":"2100","apAccount":"2000","ppvAccount":"5100","adjustmentAccount":"5000"}`
}

func TestGLRoutes_RequireTheirDuties(t *testing.T) {
	h, _, _ := ledgerServer()
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/costing/posting-profiles", profileBody(testLE, ids.New())},
		{http.MethodPost, "/v1/costing/gl-postings", `{"legalEntityId":"` + testLE + `"}`},
		{http.MethodPost, "/v1/costing/closes", `{"legalEntityId":"` + testLE + `","periodEnd":"2026-03-31"}`},
	} {
		if rec := call(h, tc.method, tc.path, tc.body, allDuties, nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s without its duty: status %d, want 403", tc.path, rec.Code)
		}
	}
}

func TestGLRoutes_RefuseWithoutALedger(t *testing.T) {
	h, _ := server()
	rec := call(h, http.MethodPost, "/v1/costing/gl-postings", `{"legalEntityId":"`+testLE+`"}`, glDuties, nil)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), CodeGLUnavailable) {
		t.Fatalf("status %d: %s, want 503 %s", rec.Code, rec.Body.String(), CodeGLUnavailable)
	}
}

func TestSavePostingProfile_PinsTheLedgerToTheLegalEntity(t *testing.T) {
	h, fs, fake := ledgerServer()
	own, foreign := ids.New(), ids.New()
	fake.AddLedger(own, testLE)
	fake.AddLedger(foreign, ids.New())
	if rec := call(h, http.MethodPost, "/v1/costing/posting-profiles", profileBody(testLE, foreign), glDuties, nil); rec.Code != http.StatusConflict {
		t.Errorf("foreign ledger status %d: %s, want 409", rec.Code, rec.Body.String())
	}
	if rec := call(h, http.MethodPost, "/v1/costing/posting-profiles", profileBody(testLE, ids.New()), glDuties, nil); rec.Code != http.StatusConflict {
		t.Errorf("unknown ledger status %d, want 409", rec.Code)
	}
	if fs.profile.GLLedgerID != "" {
		t.Fatal("a refused profile was stored")
	}
	if rec := call(h, http.MethodPost, "/v1/costing/posting-profiles", profileBody(testLE, own), glDuties, nil); rec.Code != http.StatusOK || fs.profile.GLLedgerID != own {
		t.Errorf("own ledger status %d: %s", rec.Code, rec.Body.String())
	}
	bad := strings.Replace(profileBody(testLE, own), `"1400"`, `"bad account"`, 1)
	if rec := call(h, http.MethodPost, "/v1/costing/posting-profiles", bad, glDuties, nil); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "inventoryAccount") {
		t.Errorf("bad account status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGLPostingAndClose_TakeOnlyTheEntityAndRefuseOtherEntities(t *testing.T) {
	h, fs, _ := ledgerServer()
	if rec := call(h, http.MethodPost, "/v1/costing/gl-postings", `{"legalEntityId":"`+testLE+`","amount":"1000000"}`, glDuties, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("a body with an amount: status %d, want 400 (unknown field)", rec.Code)
	}
	if rec := call(h, http.MethodPost, "/v1/costing/gl-postings", `{"legalEntityId":"`+ids.New()+`"}`, glDuties, nil); rec.Code != http.StatusForbidden {
		t.Errorf("other entity status %d, want 403", rec.Code)
	}
	if rec := call(h, http.MethodPost, "/v1/costing/gl-postings", `{"legalEntityId":"`+testLE+`"}`, glDuties, nil); rec.Code != http.StatusOK || len(fs.glRuns) != 1 {
		t.Fatalf("post status %d, runs %d", rec.Code, len(fs.glRuns))
	}
	if rec := call(h, http.MethodPost, "/v1/costing/closes", `{"legalEntityId":"`+testLE+`","periodEnd":"31/03/2026"}`, glDuties, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad date status %d, want 400", rec.Code)
	}
	if rec := call(h, http.MethodPost, "/v1/costing/closes", `{"legalEntityId":"`+testLE+`","periodEnd":"2026-03-31"}`, glDuties, nil); rec.Code != http.StatusOK || len(fs.closed) != 1 {
		t.Fatalf("close status %d, closed %d", rec.Code, len(fs.closed))
	}
}

func TestLedgerTieOut_ReportsParkedVouchers(t *testing.T) {
	h, fs, _ := ledgerServer()
	rec := call(h, http.MethodGet, "/v1/costing/value-report:ledger-tie-out?legalEntityId="+testLE+"&asOf=2026-03-31", "", glDuties, nil)
	m, _ := decodeMap(rec)
	if rec.Code != http.StatusOK || m["ties"] != false || m["parkedVouchers"] != float64(1) || fs.tieOutFor != testLE+"2026-03-31" {
		t.Fatalf("status %d body %v", rec.Code, m)
	}
	if rec := call(h, http.MethodGet, "/v1/costing/value-report:ledger-tie-out?legalEntityId="+testLE, "", glDuties, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("missing asOf status %d, want 400", rec.Code)
	}
}

func TestRunClose_CountsOnlyNewlyCompletedClosesAndKeepsRefusalCodesApart(t *testing.T) {
	fs := &fakeStore{versions: map[string]store.CostingVersion{}}
	a := New(fs).WithLedger(ledgerclient.NewFake())
	r := httpx.NewRouter()
	a.Register(r)
	body := `{"legalEntityId":"` + testLE + `","periodEnd":"2026-03-31"}`
	call(r, http.MethodPost, "/v1/costing/closes", body, glDuties, nil)
	fs.replayed = true
	call(r, http.MethodPost, "/v1/costing/closes", body, glDuties, nil)
	metrics := httptest.NewRecorder()
	a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "costing_closes_completed_total 1\n") {
		t.Errorf("metrics %s, want one completed close counted (the replay is not one)", metrics.Body.String())
	}
	fs.closeErr = &ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: "LEDGER_ACCOUNT_UNKNOWN"}
	rec := call(r, http.MethodPost, "/v1/costing/closes", body, glDuties, nil)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), CodeGLRefused) || strings.Contains(rec.Body.String(), CodeGLUnavailable) {
		t.Errorf("ledger refusal status %d: %s, want 502 %s", rec.Code, rec.Body.String(), CodeGLRefused)
	}
}
