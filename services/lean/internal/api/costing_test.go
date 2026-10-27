package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/lean/internal/domain"
	"github.com/BenjaminSRussell/passage/services/lean/internal/store"
)

const periodID = "77777777-0000-4000-8000-000000000001"

var (
	periodsPath = "/v1/lean/flows/" + flowID + "/costing-periods"
	settlePath  = periodsPath + "/" + periodID + ":settle"
)

const openBody = `{"periodStart":"2026-09-01","periodEnd":"2026-09-30","currency":"USD"}`

func TestOpenPeriod_PassesDatesCurrencyAndKey(t *testing.T) {
	fs := &fakeStore{}
	rec := call{method: "POST", path: periodsPath, body: openBody, duties: DutyFlowSettle, headers: key("p-1")}.do(newRouter(fs))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	if fs.gotOpen.FlowID != flowID || fs.gotOpen.PeriodStart.Format(store.DateLayout) != "2026-09-01" ||
		fs.gotOpen.PeriodEnd.Format(store.DateLayout) != "2026-09-30" || fs.gotOpen.Currency != "USD" || fs.gotIdem.Key != "p-1" || fs.gotIdem.Fingerprint == "" {
		t.Fatalf("open %+v idem %+v", fs.gotOpen, fs.gotIdem)
	}
	fs.replay = true
	again := call{method: "POST", path: periodsPath, body: openBody, duties: DutyFlowSettle, headers: key("p-1")}.do(newRouter(fs))
	if again.Code != http.StatusOK || again.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay %d %v", again.Code, again.Header())
	}
}

func TestOpenPeriod_ValidationDutiesAndConflicts(t *testing.T) {
	cases := map[string]struct{ body, field string }{
		"end before start": {`{"periodStart":"2026-09-30","periodEnd":"2026-09-01","currency":"USD"}`, "periodEnd"},
		"bad start":        {`{"periodStart":"09/01/2026","periodEnd":"2026-09-30","currency":"USD"}`, "periodStart"},
		"missing end":      {`{"periodStart":"2026-09-01","currency":"USD"}`, "periodEnd"},
		"lower currency":   {`{"periodStart":"2026-09-01","periodEnd":"2026-09-30","currency":"usd"}`, "currency"},
		"no currency":      {`{"periodStart":"2026-09-01","periodEnd":"2026-09-30"}`, "currency"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fs := &fakeStore{}
			rec := call{method: "POST", path: periodsPath, body: c.body, duties: DutyFlowSettle, headers: key("p-2")}.do(newRouter(fs))
			if rec.Code != http.StatusBadRequest || problemOf(t, rec).Errors[c.field] == "" {
				t.Fatalf("status %d %s", rec.Code, rec.Body)
			}
			if fs.gotOpen.FlowID != "" {
				t.Fatal("store called for an invalid period")
			}
		})
	}
	if rec := (call{method: "POST", path: periodsPath, body: openBody, duties: DutyFlowSettle}).do(newRouter(&fakeStore{})); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: %d", rec.Code)
	}
	if rec := (call{method: "POST", path: periodsPath, body: openBody, duties: DutyFlowWrite, headers: key("p-3")}).do(newRouter(&fakeStore{})); rec.Code != http.StatusForbidden {
		t.Fatalf("flow-write duty opened a period: %d", rec.Code)
	}
	if rec := (call{method: "POST", path: "/v1/lean/flows/nope/costing-periods", body: openBody, duties: DutyFlowSettle, headers: key("p-4")}).do(newRouter(&fakeStore{})); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad flow id: %d", rec.Code)
	}
	fs := &fakeStore{err: httpx.NewProblem(http.StatusConflict, store.CodePeriodOverlap, "overlaps")}
	rec := call{method: "POST", path: periodsPath, body: openBody, duties: DutyFlowSettle, headers: key("p-5")}.do(newRouter(fs))
	if rec.Code != http.StatusConflict || problemOf(t, rec).Code != "LEAN-0109" {
		t.Fatalf("overlap: %d %s", rec.Code, rec.Body)
	}
}

func TestListPeriods_ReadDutyAndLimit(t *testing.T) {
	fs := &fakeStore{}
	rec := call{method: "GET", path: periodsPath + "?limit=12", duties: DutyRead}.do(newRouter(fs))
	if rec.Code != http.StatusOK || fs.gotFlowID != flowID || fs.gotLimit != 12 || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("list %d %s %+v", rec.Code, rec.Body, fs)
	}
	if rec := (call{method: "GET", path: periodsPath + "?limit=0", duties: DutyRead}).do(newRouter(&fakeStore{})); rec.Code != http.StatusBadRequest {
		t.Fatalf("limit 0: %d", rec.Code)
	}
	if rec := (call{method: "GET", path: periodsPath, duties: DutyKanbanWork}).do(newRouter(&fakeStore{})); rec.Code != http.StatusForbidden {
		t.Fatalf("no read duty: %d", rec.Code)
	}
	fs = &fakeStore{err: httpx.NewProblem(http.StatusNotFound, store.CodeFlowNotFound, "production flow not found")}
	if rec := (call{method: "GET", path: periodsPath, duties: DutyRead}).do(newRouter(fs)); rec.Code != http.StatusNotFound || problemOf(t, rec).Code != "LEAN-0101" {
		t.Fatalf("missing flow: %d %s", rec.Code, rec.Body)
	}
}

const settleBody = `{"version":1,"materialCost":"900.10","conversionCost":"300","outputValue":"1150.25"}`

func TestSettlePeriod_PassesAmountsVersionAndKey(t *testing.T) {
	fs := &fakeStore{}
	rec := call{method: "POST", path: settlePath, body: settleBody, duties: DutyFlowSettle, headers: key("s-1")}.do(newRouter(fs))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	g := fs.gotSettle
	if g.FlowID != flowID || g.PeriodID != periodID || g.Version != 1 || !g.MaterialCost.Equal(decimal.RequireFromString("900.10")) ||
		!g.ConversionCost.Equal(decimal.RequireFromString("300")) || !g.OutputValue.Equal(decimal.RequireFromString("1150.25")) || fs.gotIdem.Key != "s-1" {
		t.Fatalf("settle %+v idem %+v", g, fs.gotIdem)
	}
	fs.replay = true
	if again := (call{method: "POST", path: settlePath, body: settleBody, duties: DutyFlowSettle, headers: key("s-1")}).do(newRouter(fs)); again.Code != http.StatusOK || again.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay %d %v", again.Code, again.Header())
	}
}

func TestSettlePeriod_ValidationAndRefusals(t *testing.T) {
	cases := map[string]struct{ body, field string }{
		"no version":      {`{"materialCost":"1","conversionCost":"1","outputValue":"1"}`, "version"},
		"no material":     {`{"version":1,"conversionCost":"1","outputValue":"1"}`, "materialCost"},
		"negative output": {`{"version":1,"materialCost":"1","conversionCost":"1","outputValue":"-1"}`, "outputValue"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fs := &fakeStore{}
			rec := call{method: "POST", path: settlePath, body: c.body, duties: DutyFlowSettle, headers: key("s-2")}.do(newRouter(fs))
			if rec.Code != http.StatusBadRequest || problemOf(t, rec).Errors[c.field] == "" {
				t.Fatalf("status %d %s", rec.Code, rec.Body)
			}
			if fs.gotSettle.PeriodID != "" {
				t.Fatal("store called for an invalid settlement")
			}
		})
	}
	if rec := (call{method: "POST", path: periodsPath + "/x:settle", body: settleBody, duties: DutyFlowSettle, headers: key("s-3")}).do(newRouter(&fakeStore{})); rec.Code != http.StatusBadRequest || problemOf(t, rec).Errors["periodId"] == "" {
		t.Fatalf("bad period id: %d %s", rec.Code, rec.Body)
	}
	if rec := (call{method: "POST", path: settlePath, body: settleBody, duties: DutyRead, headers: key("s-4")}).do(newRouter(&fakeStore{})); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty settled: %d", rec.Code)
	}
	refusals := map[string]struct {
		err    error
		status int
	}{
		"LEAN-0003": {&domain.Error{Code: domain.CodeOpenKanbans, Detail: "period cannot be settled: 1 kanbans open (K-0001)"}, http.StatusConflict},
		"COM-0001":  {&domain.Error{Code: domain.CodeInvalid, Detail: "conversionCost has more than 2 decimals for USD"}, http.StatusBadRequest},
		"LEAN-0111": {httpx.NewProblem(http.StatusConflict, store.CodePeriodSettled, "already settled"), http.StatusConflict},
		"LEAN-0112": {httpx.NewProblem(http.StatusConflict, store.CodePeriodNotEnded, "settles after its last day"), http.StatusConflict},
		"LEAN-0110": {httpx.NewProblem(http.StatusNotFound, store.CodePeriodNotFound, "not found"), http.StatusNotFound},
	}
	for code, c := range refusals {
		rec := call{method: "POST", path: settlePath, body: settleBody, duties: DutyFlowSettle, headers: key("s-" + code)}.do(newRouter(&fakeStore{err: c.err}))
		if rec.Code != c.status || problemOf(t, rec).Code != code {
			t.Errorf("%s: %d %s", code, rec.Code, rec.Body)
		}
	}
}
